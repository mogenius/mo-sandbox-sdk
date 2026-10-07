package mogenius

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
)

func fileJSON(name string) map[string]any {
	return map[string]any{
		"name": name, "path": "/w/" + name, "isDir": false, "size": 12, "modTime": "2026-10-01T12:00:00Z",
		"mode": "0644", "permissions": "-rw-r--r--", "owner": "sandbox", "group": "users", "mimeType": "text/plain",
	}
}

func TestFileSystemRequests(t *testing.T) {
	f := newFakePlatform(t,
		reply{body: []any{fileJSON("a.txt")}},
		reply{body: fileJSON("a.txt")},
		reply{status: 201},
		reply{status: 200},
		reply{status: 201},
		reply{status: 201, body: fileJSON("a.txt")},
		reply{body: map[string]any{"files": []any{"/w/a.py"}, "truncated": true}},
		reply{body: map[string]any{"matches": []any{map[string]any{"file": "/w/a.py", "line": 3, "content": "import os"}}, "truncated": false}},
		reply{status: 201, body: []any{
			map[string]any{"file": "/w/a.py", "success": true, "error": nil},
			map[string]any{"file": "/w/b.py", "success": false, "error": "permission denied"},
		}},
	)
	fs := f.sandbox(t, nil).FileSystem
	ctx := context.Background()

	files, err := fs.ListFiles(ctx, "/w")
	if err != nil {
		t.Fatal(err)
	}
	file := files[0]
	if file.Name != "a.txt" || file.Path != "/w/a.txt" || file.Size != 12 || file.Mode != "0644" || file.IsDirectory ||
		!file.ModifiedTime.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) || file.Permissions != "-rw-r--r--" ||
		file.Owner != "sandbox" || file.Group != "users" || file.MimeType != "text/plain" {
		t.Fatalf("file %+v", file)
	}
	if info, err := fs.GetFileInfo(ctx, "/w/a.txt"); err != nil || info.Name != "a.txt" {
		t.Fatalf("info %+v, %v", info, err)
	}
	if err := fs.CreateFolder(ctx, "/w/new", options.WithMode("755")); err != nil {
		t.Fatal(err)
	}
	if err := fs.DeleteFile(ctx, "/w/new", true); err != nil {
		t.Fatal(err)
	}
	if err := fs.MoveFiles(ctx, "/w/a", "/w/b"); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetFilePermissions(ctx, "/w/a.txt", options.WithPermissionMode("600"), options.WithOwner("root")); err != nil {
		t.Fatal(err)
	}
	search, err := fs.SearchFiles(ctx, "/w", "*.py")
	if err != nil || !reflect.DeepEqual(search, map[string]any{"files": []string{"/w/a.py"}, "truncated": true}) {
		t.Fatalf("search %#v, %v", search, err)
	}
	find, err := fs.FindFiles(ctx, "/w", "import")
	if err != nil || !reflect.DeepEqual(find, []map[string]any{{"file": "/w/a.py", "line": int32(3), "content": "import os"}}) {
		t.Fatalf("find %#v, %v", find, err)
	}
	replace, err := fs.ReplaceInFiles(ctx, []string{"/w/a.py", "/w/b.py"}, "os", "sys")
	want := []map[string]any{{"file": "/w/a.py", "success": true}, {"file": "/w/b.py", "success": false, "error": "permission denied"}}
	if err != nil || !reflect.DeepEqual(replace, want) {
		t.Fatalf("replace %#v, %v", replace, err)
	}

	calls := f.recorded()
	route := "/sandbox/agent-sandbox/default-abc12/toolbox/files"
	for i, want := range []struct{ method, path, query string }{
		{http.MethodGet, route, "path=%2Fw"},
		{http.MethodGet, route + "/info", "path=%2Fw%2Fa.txt"},
		{http.MethodPost, route + "/folder", "mode=755&path=%2Fw%2Fnew"},
		{http.MethodDelete, route, "path=%2Fw%2Fnew&recursive=true"},
		{http.MethodPost, route + "/move", "destination=%2Fw%2Fb&source=%2Fw%2Fa"},
		{http.MethodPost, route + "/permissions", "mode=600&owner=root&path=%2Fw%2Fa.txt"},
		{http.MethodGet, route + "/search", "path=%2Fw&pattern=%2A.py"},
		{http.MethodGet, route + "/find", "path=%2Fw&pattern=import"},
		{http.MethodPost, route + "/replace", ""},
	} {
		expectCall(t, calls[i], want.method, want.path)
		if got := calls[i].query.Encode(); got != want.query {
			t.Errorf("call %d query %s, want %s", i, got, want.query)
		}
	}
	if body := calls[8].json(t); !reflect.DeepEqual(body, map[string]any{"files": []any{"/w/a.py", "/w/b.py"}, "pattern": "os", "newValue": "sys"}) {
		t.Fatalf("replace body %v", body)
	}
}

func TestDownloadFileThroughTheLink(t *testing.T) {
	f := newFakePlatform(t)
	f.answers = []any{
		func(recordedCall) reply {
			return reply{status: 201, body: map[string]any{"url": f.server.URL + "/dl/one-time", "expiresInSeconds": 60}}
		},
		reply{raw: []byte("hello file"), header: map[string]string{"Accept-Ranges": "bytes", "ETag": `"v1"`}},
	}
	local := filepath.Join(t.TempDir(), "out.txt")

	data, err := f.sandbox(t, nil).FileSystem.DownloadFile(context.Background(), "/w/a.txt", &local)
	if err != nil || string(data) != "hello file" {
		t.Fatalf("data %q, %v", data, err)
	}
	if written, _ := os.ReadFile(local); string(written) != "hello file" {
		t.Fatalf("local file %q", written)
	}

	calls := f.recorded()
	expectCall(t, calls[0], http.MethodPost, "/sandbox/agent-sandbox/default-abc12/toolbox/files/download-link")
	if calls[0].query.Get("path") != "/w/a.txt" || calls[0].header.Get("Authorization") == "" {
		t.Fatalf("link request %+v", calls[0])
	}
	// the link is its own credential
	expectCall(t, calls[1], http.MethodGet, "/dl/one-time")
	if calls[1].header.Get("Authorization") != "" || calls[1].header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("download headers %v", calls[1].header)
	}
}

func TestDownloadFileStreamResumesWhereTheConnectionBroke(t *testing.T) {
	content := []byte("0123456789")
	f := newFakePlatform(t)
	f.answers = []any{
		func(recordedCall) reply {
			return reply{status: 201, body: map[string]any{"url": f.server.URL + "/dl/one-time", "expiresInSeconds": 60}}
		},
		// announces ten bytes and sends four: the server drops the connection
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write(content[:4])
		}),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 4-9/10")
			w.Header().Set("Content-Length", "6")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[4:])
		}),
	}
	fs := f.sandbox(t, nil).FileSystem
	fs.resumeDelays = []time.Duration{0, 0}
	var progress []DownloadProgress

	stream, err := fs.DownloadFileStream(context.Background(), "/w/big.bin",
		WithDownloadProgress(func(p DownloadProgress) { progress = append(progress, p) }))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil || !bytes.Equal(data, content) {
		t.Fatalf("data %q, %v", data, err)
	}

	resume := f.recorded()[2]
	if resume.header.Get("Range") != "bytes=4-" || resume.header.Get("If-Range") != `"v1"` {
		t.Fatalf("resume headers %v", resume.header)
	}
	if last := progress[len(progress)-1]; last.BytesReceived != 10 || last.TotalBytes != 10 {
		t.Fatalf("progress %+v", progress)
	}
}

func TestDownloadFileStreamFailsWhenTheFileChanged(t *testing.T) {
	f := newFakePlatform(t)
	f.answers = []any{
		func(recordedCall) reply {
			return reply{status: 201, body: map[string]any{"url": f.server.URL + "/dl/one-time", "expiresInSeconds": 60}}
		},
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", "10")
			_, _ = w.Write([]byte("0123"))
		}),
		// a 200 instead of a 206: the whole, changed file
		reply{raw: []byte("new content")},
	}
	fs := f.sandbox(t, nil).FileSystem
	fs.resumeDelays = []time.Duration{0}

	_, err := fs.DownloadFile(context.Background(), "/w/big.bin", nil)
	if err == nil || !strings.Contains(err.Error(), "could not continue at byte 4") {
		t.Fatalf("got %v", err)
	}
}

func TestDownloadFallsBackToTheBufferedRoute(t *testing.T) {
	f := newFakePlatform(t,
		reply{status: 404, body: map[string]any{
			"statusCode": 404, "message": "Cannot POST /sandbox/agent-sandbox/default-abc12/toolbox/files/download-link", "error": "Not Found",
		}},
		reply{raw: []byte("buffered")},
	)

	data, err := f.sandbox(t, nil).FileSystem.DownloadFile(context.Background(), "/w/a.txt", nil)
	if err != nil || string(data) != "buffered" {
		t.Fatalf("data %q, %v", data, err)
	}
	call := f.recorded()[1]
	expectCall(t, call, http.MethodGet, "/sandbox/agent-sandbox/default-abc12/toolbox/files/download")
	if call.query.Get("path") != "/w/a.txt" {
		t.Fatalf("query %v", call.query)
	}
}

func TestDownloadOfAMissingFileIsNotAFallback(t *testing.T) {
	f := newFakePlatform(t, reply{status: 404, body: map[string]any{
		"statusCode": 404, "errorCode": "FILE_NOT_FOUND", "source": "operator", "message": "no such file",
	}})

	_, err := f.sandbox(t, nil).FileSystem.DownloadFile(context.Background(), "/w/nope", nil)
	var notFound *sdkerrors.MogeniusNotFoundError
	if !errors.As(err, &notFound) || notFound.ErrorCode != "FILE_NOT_FOUND" || len(f.recorded()) != 1 {
		t.Fatalf("got %v", err)
	}
}

// uploadedPart reads the single file part of a recorded multipart upload.
func uploadedPart(t *testing.T, call recordedCall) (field, filename, content string) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(call.header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("content type %q", call.header.Get("Content-Type"))
	}
	part, err := multipart.NewReader(bytes.NewReader(call.body), params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(part)
	return part.FormName(), part.FileName(), string(data)
}

func TestUploadFile(t *testing.T) {
	uploaded := reply{status: 201, body: []any{map[string]any{"path": "/w/data/a.txt", "success": true, "error": nil}}}
	f := newFakePlatform(t,
		uploaded,
		uploaded,
		reply{status: 201, body: []any{map[string]any{"path": "/w/x", "success": false, "error": "read-only file system"}}},
	)
	fs := f.sandbox(t, nil).FileSystem
	ctx := context.Background()
	local := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(local, []byte("from disk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fs.UploadFile(ctx, []byte("hello"), "/w/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.UploadFile(ctx, local, "/w/data/b.txt"); err != nil {
		t.Fatal(err)
	}
	err := fs.UploadFile(ctx, []byte("x"), "/w/x")
	var base *sdkerrors.MogeniusError
	if !errors.As(err, &base) || base.Source != sdkerrors.SourceOperator || !strings.Contains(base.Message, "read-only file system") {
		t.Fatalf("failed upload: got %v", err)
	}
	var validation *sdkerrors.MogeniusValidationError
	if err := fs.UploadFile(ctx, 42, "/w/x"); !errors.As(err, &validation) {
		t.Fatalf("source type: got %v", err)
	}

	calls := f.recorded()
	expectCall(t, calls[0], http.MethodPost, "/sandbox/agent-sandbox/default-abc12/toolbox/files/upload")
	if calls[0].query.Get("path") != "/w/data/a.txt" || calls[0].length != int64(len(calls[0].body)) {
		t.Fatalf("upload request %+v", calls[0])
	}
	if field, filename, content := uploadedPart(t, calls[0]); field != "file" || filename != "a.txt" || content != "hello" {
		t.Fatalf("part %s %s %q", field, filename, content)
	}
	if _, filename, content := uploadedPart(t, calls[1]); filename != "b.txt" || content != "from disk" {
		t.Fatalf("part %s %q", filename, content)
	}
}

func TestUploadFileStreamOfUnknownSize(t *testing.T) {
	f := newFakePlatform(t, reply{status: 201, body: []any{map[string]any{"path": "/w/s.txt", "success": true}}})
	source := io.MultiReader(strings.NewReader("chunk-1 "), strings.NewReader("chunk-2"))
	var sent int64

	err := f.sandbox(t, nil).FileSystem.UploadFileStream(context.Background(), source, "/w/s.txt",
		WithUploadProgress(func(p UploadProgress) { sent = p.BytesSent }))
	if err != nil {
		t.Fatal(err)
	}

	call := f.recorded()[0]
	if _, _, content := uploadedPart(t, call); content != "chunk-1 chunk-2" || call.length != -1 || sent != 15 {
		t.Fatalf("content %q, length %d, sent %d", content, call.length, sent)
	}
}
