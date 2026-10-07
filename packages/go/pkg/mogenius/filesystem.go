package mogenius

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// defaultResumeDelays are the pauses before each attempt to resume a broken download.
var defaultResumeDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

var contentRangeStart = regexp.MustCompile(`^bytes (\d+)-\d+/\d+$`)

// FileSystemService is the file system of a sandbox. Paths are absolute
// inside the container or relative to its working directory. Every call goes
// through the platform API to the operator, which runs the file operation
// inside the pod; nothing is installed in the image for it.
type FileSystemService struct {
	sandbox      *Sandbox
	resumeDelays []time.Duration
}

// fileInfoBody is one file entry as the platform returns it.
type fileInfoBody struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	IsDir       bool    `json:"isDir"`
	Size        int64   `json:"size"`
	ModTime     string  `json:"modTime"`
	Mode        string  `json:"mode"`
	Permissions string  `json:"permissions"`
	Owner       string  `json:"owner"`
	Group       string  `json:"group"`
	MimeType    *string `json:"mimeType"`
}

type uploadResult struct {
	Path    string  `json:"path"`
	Success bool    `json:"success"`
	Error   *string `json:"error"`
}

type downloadLink struct {
	URL              string `json:"url"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
}

// call sends one request to the pod route of suffix, in the sandbox's own
// container (the pod route would take the pod's first): files are a pod feature.
func (f *FileSystemService) call(ctx context.Context, method, suffix string, values url.Values, body, out any) error {
	podName, container, err := f.sandbox.pod(ctx)
	if err != nil {
		return err
	}
	if values == nil {
		values = url.Values{}
	}
	if container != "" {
		values.Set("container", container)
	}
	path := "/resource/files/" + segment(f.sandbox.Namespace) + "/" + segment(podName) + suffix
	return f.sandbox.api.do(ctx, method, path, values, body, out)
}

// ListFiles returns the entries directly below path (empty: the working directory).
func (f *FileSystemService) ListFiles(ctx context.Context, path string) ([]*types.FileInfo, error) {
	var bodies []fileInfoBody
	if err := f.call(ctx, http.MethodGet, "", query("path", path), nil, &bodies); err != nil {
		return nil, err
	}
	files := make([]*types.FileInfo, len(bodies))
	for i, body := range bodies {
		files[i] = toFileInfo(body)
	}
	return files, nil
}

// GetFileInfo returns one file's or folder's details.
func (f *FileSystemService) GetFileInfo(ctx context.Context, path string) (*types.FileInfo, error) {
	var body fileInfoBody
	if err := f.call(ctx, http.MethodGet, "/info", query("path", path), nil, &body); err != nil {
		return nil, err
	}
	return toFileInfo(body), nil
}

// CreateFolder creates the folder and its parents.
func (f *FileSystemService) CreateFolder(ctx context.Context, path string, opts ...func(*options.CreateFolder)) error {
	folderOpts := &options.CreateFolder{}
	for _, opt := range opts {
		opt(folderOpts)
	}
	return f.call(ctx, http.MethodPost, "/folder", query("path", path, "mode", deref(folderOpts.Mode)), nil, nil)
}

// DeleteFile removes a file, or a folder — only when empty unless recursive.
func (f *FileSystemService) DeleteFile(ctx context.Context, path string, recursive bool) error {
	return f.call(ctx, http.MethodDelete, "", query("path", path, "recursive", strconv.FormatBool(recursive)), nil, nil)
}

// MoveFiles moves or renames; the destination's folder must exist.
func (f *FileSystemService) MoveFiles(ctx context.Context, source, destination string) error {
	return f.call(ctx, http.MethodPost, "/move", query("source", source, "destination", destination), nil, nil)
}

// SetFilePermissions changes mode, owner and group independently; what is not given stays.
func (f *FileSystemService) SetFilePermissions(ctx context.Context, path string, opts ...func(*options.SetFilePermissions)) error {
	permOpts := &options.SetFilePermissions{}
	for _, opt := range opts {
		opt(permOpts)
	}
	return f.call(ctx, http.MethodPost, "/permissions",
		query("path", path, "mode", deref(permOpts.Mode), "owner", deref(permOpts.Owner), "group", deref(permOpts.Group)), nil, nil)
}

// SearchFiles finds names below path matching a shell glob, e.g. *.py. The
// result is a map[string]any with files ([]string) and, mogenius, truncated
// (bool): more matched than were returned.
func (f *FileSystemService) SearchFiles(ctx context.Context, path, pattern string) (any, error) {
	var body struct {
		Files     []string `json:"files"`
		Truncated bool     `json:"truncated"`
	}
	if err := f.call(ctx, http.MethodGet, "/search", query("path", path, "pattern", pattern), nil, &body); err != nil {
		return nil, err
	}
	if body.Files == nil {
		body.Files = []string{}
	}
	return map[string]any{"files": body.Files, "truncated": body.Truncated}, nil
}

// FindFiles finds lines below path matching a grep pattern. The result is a
// []map[string]any, one per line, with file, line (int32) and content.
func (f *FileSystemService) FindFiles(ctx context.Context, path, pattern string) (any, error) {
	var body struct {
		Matches []struct {
			File    string `json:"file"`
			Line    int32  `json:"line"`
			Content string `json:"content"`
		} `json:"matches"`
	}
	if err := f.call(ctx, http.MethodGet, "/find", query("path", path, "pattern", pattern), nil, &body); err != nil {
		return nil, err
	}
	matches := make([]map[string]any, len(body.Matches))
	for i, match := range body.Matches {
		matches[i] = map[string]any{"file": match.File, "line": match.Line, "content": match.Content}
	}
	return matches, nil
}

// ReplaceInFiles replaces every literal occurrence of pattern in each file.
// The result is a []map[string]any, one per file, with file, success and,
// on a failure, error.
func (f *FileSystemService) ReplaceInFiles(ctx context.Context, files []string, pattern, newValue string) (any, error) {
	var body []struct {
		File    string  `json:"file"`
		Success bool    `json:"success"`
		Error   *string `json:"error"`
	}
	request := map[string]any{"files": files, "pattern": pattern, "newValue": newValue}
	if err := f.call(ctx, http.MethodPost, "/replace", nil, request, &body); err != nil {
		return nil, err
	}
	results := make([]map[string]any, len(body))
	for i, result := range body {
		entry := map[string]any{"file": result.File, "success": result.Success}
		if result.Error != nil {
			entry["error"] = *result.Error
		}
		results[i] = entry
	}
	return results, nil
}

// UploadFile writes source to destination, creating parent folders. source
// is []byte (the content) or string (the path of a local file).
func (f *FileSystemService) UploadFile(ctx context.Context, source any, destination string) error {
	switch src := source.(type) {
	case []byte:
		return f.upload(ctx, bytes.NewReader(src), int64(len(src)), destination, nil)
	case string:
		file, err := os.Open(src)
		if err != nil {
			return sdkerrors.NewMogeniusError(fmt.Sprintf("Failed to read file: %v", err), 0, nil)
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil {
			return sdkerrors.NewMogeniusError(fmt.Sprintf("Failed to read file: %v", err), 0, nil)
		}
		return f.upload(ctx, file, stat.Size(), destination, nil)
	default:
		return sdkerrors.NewMogeniusValidationError(fmt.Sprintf("UploadFile takes []byte or a local file path (string), not %T.", source), nil)
	}
}

// UploadStreamOption configures UploadFileStream.
type UploadStreamOption func(*uploadStreamConfig)

// UploadProgress is how far an upload got.
type UploadProgress struct {
	// BytesSent is the cumulative number of bytes read from the source.
	BytesSent int64
}

type uploadStreamConfig struct {
	onProgress func(UploadProgress)
}

// WithUploadProgress calls fn as the upload reads its source.
func WithUploadProgress(fn func(UploadProgress)) UploadStreamOption {
	return func(c *uploadStreamConfig) {
		c.onProgress = fn
	}
}

// UploadFileStream writes what source yields to remotePath without holding it
// in memory. A source of unknown size is sent chunked.
func (f *FileSystemService) UploadFileStream(ctx context.Context, source io.Reader, remotePath string, opts ...UploadStreamOption) error {
	config := &uploadStreamConfig{}
	for _, opt := range opts {
		opt(config)
	}
	size := int64(-1)
	switch src := source.(type) {
	case interface{ Len() int }:
		size = int64(src.Len())
	case *os.File:
		if stat, err := src.Stat(); err == nil && stat.Mode().IsRegular() {
			if offset, err := src.Seek(0, io.SeekCurrent); err == nil {
				size = stat.Size() - offset
			}
		}
	}
	return f.upload(ctx, source, size, remotePath, config.onProgress)
}

func (f *FileSystemService) upload(ctx context.Context, content io.Reader, size int64, destination string, progress func(UploadProgress)) error {
	if progress != nil {
		content = &countingReader{reader: content, report: func(n int64) { progress(UploadProgress{BytesSent: n}) }}
	}
	form, err := newFormBody("file", basename(destination), content, size)
	if err != nil {
		return sdkerrors.NewMogeniusError("Failed to build the upload: "+err.Error(), 0, nil)
	}
	var results []uploadResult
	if err := f.call(ctx, http.MethodPost, "/upload", query("path", destination), form, &results); err != nil {
		return err
	}
	for _, result := range results {
		if !result.Success {
			reason := "unknown error"
			if result.Error != nil {
				reason = *result.Error
			}
			failure := sdkerrors.NewMogeniusError(fmt.Sprintf("Upload to %s failed: %s", result.Path, reason), 0, nil)
			failure.Source = sdkerrors.SourceOperator
			return failure
		}
	}
	return nil
}

// DownloadFile returns the file's bytes and, when localPath is not nil, also
// writes them there. A folder comes back as a gzipped tar.
func (f *FileSystemService) DownloadFile(ctx context.Context, remotePath string, localPath *string) ([]byte, error) {
	stream, err := f.DownloadFileStream(ctx, remotePath)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	if localPath != nil {
		if err := os.WriteFile(*localPath, data, 0o644); err != nil {
			return nil, sdkerrors.NewMogeniusError(fmt.Sprintf("Failed to write file: %v", err), 0, nil)
		}
	}
	return data, nil
}

// DownloadStreamOption configures DownloadFileStream.
type DownloadStreamOption func(*downloadStreamConfig)

// DownloadProgress is how far a download got.
type DownloadProgress struct {
	// BytesReceived is the cumulative number of bytes read so far.
	BytesReceived int64
	// TotalBytes is the size of the file; zero when unknown.
	TotalBytes int64
}

type downloadStreamConfig struct {
	onProgress func(DownloadProgress)
}

// WithDownloadProgress calls fn as the download is read.
func WithDownloadProgress(fn func(DownloadProgress)) DownloadStreamOption {
	return func(c *downloadStreamConfig) {
		c.onProgress = fn
	}
}

// DownloadFileStream returns the file as a stream, chunked from the container
// to the caller without any station holding it whole — for files too big for
// memory. The caller closes it. The platform hands out a short-lived link
// that is fetched without auth headers.
//
// A file (not a folder) resumes on its own: when the connection drops or ends
// early, the stream asks the same link for the rest with a Range request and
// carries on, up to five times with growing pauses. What the caller reads
// stays one uninterrupted, complete file.
func (f *FileSystemService) DownloadFileStream(ctx context.Context, remotePath string, opts ...DownloadStreamOption) (io.ReadCloser, error) {
	config := &downloadStreamConfig{}
	for _, opt := range opts {
		opt(config)
	}
	var link downloadLink
	if err := f.call(ctx, http.MethodPost, "/download-link", query("path", remotePath), nil, &link); err != nil {
		return nil, err
	}
	first, err := f.sandbox.api.fetchDownload(ctx, link.URL, nil)
	if err != nil {
		return nil, err
	}
	total := first.ContentLength
	etag := first.Header.Get("ETag")
	return &resumableReader{
		ctx:        ctx,
		api:        f.sandbox.api,
		link:       link.URL,
		body:       first.Body,
		total:      total,
		etag:       etag,
		resumable:  first.Header.Get("Accept-Ranges") == "bytes" && etag != "" && total >= 0,
		delays:     f.resumeDelays,
		onProgress: config.onProgress,
	}, nil
}

// resumableReader reads a download link and resumes on its own: it counts
// the bytes it handed out and, when the body breaks or ends before the
// announced length, asks the link again for the rest with Range and the
// file's ETag in If-Range. Only a 206 that continues exactly at that byte is
// spliced in: a 200 means the file changed (or the platform cannot resume),
// and since bytes already went out the stream fails instead of repeating them.
type resumableReader struct {
	ctx        context.Context
	api        *apiClient
	link       string
	body       io.ReadCloser
	total      int64
	etag       string
	resumable  bool
	received   int64
	attempts   int
	delays     []time.Duration
	onProgress func(DownloadProgress)
	err        error
}

func (r *resumableReader) Read(p []byte) (int, error) {
	for r.err == nil {
		n, err := r.body.Read(p)
		if n > 0 {
			r.received += int64(n)
			if r.onProgress != nil {
				r.onProgress(DownloadProgress{BytesReceived: r.received, TotalBytes: max(r.total, 0)})
			}
			// a body keeps returning its error; it is handled on the next read
			return n, nil
		}
		switch {
		case err == nil:
			// an empty read: read again
		case errors.Is(err, io.EOF) && !(r.resumable && r.received < r.total):
			r.err = io.EOF
		case errors.Is(err, io.EOF):
			r.err = r.resume("the connection ended early")
		default:
			r.err = r.resume(err.Error())
		}
	}
	return 0, r.err
}

func (r *resumableReader) Close() error {
	return r.body.Close()
}

// resume swaps in a body that continues where the broken one stopped; nil on success.
func (r *resumableReader) resume(cause string) error {
	if !r.resumable || r.attempts >= len(r.delays) {
		message := fmt.Sprintf("Download stopped at byte %d: %s", r.received, cause)
		if r.resumable {
			message = fmt.Sprintf("Download stopped at byte %d of %d and could not be resumed: %s", r.received, r.total, cause)
		}
		return sdkerrors.NewMogeniusError(message, 0, nil)
	}
	timer := time.NewTimer(r.delays[r.attempts])
	select {
	case <-r.ctx.Done():
		timer.Stop()
		return r.ctx.Err()
	case <-timer.C:
	}
	r.attempts++
	_ = r.body.Close()
	response, err := r.api.fetchDownload(r.ctx, r.link, &byteRange{offset: r.received, ifRange: r.etag})
	if err != nil {
		return err
	}
	match := contentRangeStart.FindStringSubmatch(response.Header.Get("Content-Range"))
	if response.StatusCode != http.StatusPartialContent || match == nil || match[1] != strconv.FormatInt(r.received, 10) {
		_ = response.Body.Close()
		return sdkerrors.NewMogeniusError(fmt.Sprintf(
			"Download could not continue at byte %d: the file changed or the platform cannot resume (HTTP %d).",
			r.received, response.StatusCode), 0, nil)
	}
	r.body = response.Body
	return nil
}

func toFileInfo(body fileInfoBody) *types.FileInfo {
	modified, _ := time.Parse(time.RFC3339Nano, body.ModTime)
	return &types.FileInfo{
		Name:         body.Name,
		Size:         body.Size,
		Mode:         body.Mode,
		ModifiedTime: modified,
		IsDirectory:  body.IsDir,
		Path:         body.Path,
		Permissions:  body.Permissions,
		Owner:        body.Owner,
		Group:        body.Group,
		MimeType:     deref(body.MimeType),
	}
}

// countingReader reports the running total of what was read through it.
type countingReader struct {
	reader io.Reader
	total  int64
	report func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if n > 0 {
		c.total += int64(n)
		c.report(c.total)
	}
	return n, err
}

func basename(path string) string {
	trimmed := strings.TrimRight(path, "/")
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
