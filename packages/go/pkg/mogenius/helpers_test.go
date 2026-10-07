package mogenius

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// recordedCall is one request the fake platform got.
type recordedCall struct {
	method string
	// path as it went over the wire, escaped
	path   string
	query  url.Values
	header http.Header
	// length is the announced Content-Length; -1 for a chunked body
	length int64
	body   []byte
}

// json decodes the recorded body.
func (c recordedCall) json(t *testing.T) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(c.body, &decoded); err != nil {
		t.Fatalf("%s %s: body is not JSON: %v (%q)", c.method, c.path, err, c.body)
	}
	return decoded
}

// reply is a canned answer: status and JSON body, or raw bytes.
type reply struct {
	status int
	body   any
	raw    []byte
	header map[string]string
}

// fakePlatform is the platform API on an httptest server. It answers from a
// queue and records what it was asked; the last answer repeats so polling
// loops have something to read. An answer is a reply, a func(recordedCall)
// reply, or an http.HandlerFunc for full control. The stream gateway at
// /xterm-stream runs gateway on each socket.
type fakePlatform struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	calls   []recordedCall
	answers []any
	gateway func(conn *websocket.Conn, r *http.Request)
	streams []url.Values
}

func newFakePlatform(t *testing.T, answers ...any) *fakePlatform {
	t.Helper()
	// a developer's shell must not leak into the tests
	for _, name := range []string{"MOGENIUS_API_KEY", "MOGENIUS_API_URL", "MOGENIUS_ORGANIZATION_ID", "MOGENIUS_CLUSTER_ID",
		"MOGENIUS_SANDBOX_NAMESPACE", "MOGENIUS_WORKSPACE_NAME", "MOGENIUS_STREAM_URL"} {
		t.Setenv(name, "")
	}
	f := &fakePlatform{t: t, answers: answers}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePlatform) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/xterm-stream" {
		f.serveGateway(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	call := recordedCall{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.Query(), header: r.Header.Clone(), length: r.ContentLength, body: body}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	var next any
	switch {
	case len(f.answers) > 1:
		next, f.answers = f.answers[0], f.answers[1:]
	case len(f.answers) == 1:
		next = f.answers[0]
	}
	f.mu.Unlock()

	var answer reply
	switch value := next.(type) {
	case nil:
	case reply:
		answer = value
	case func(recordedCall) reply:
		answer = value(call)
	case http.HandlerFunc:
		value(w, r)
		return
	default:
		f.t.Errorf("unknown answer type %T", next)
	}
	for key, value := range answer.header {
		w.Header().Set(key, value)
	}
	status := answer.status
	if status == 0 {
		status = http.StatusOK
	}
	if answer.raw != nil {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.WriteHeader(status)
		_, _ = w.Write(answer.raw)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if answer.body != nil {
		_ = json.NewEncoder(w).Encode(answer.body)
	}
}

func (f *fakePlatform) serveGateway(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		f.t.Errorf("gateway: %v", err)
		return
	}
	defer conn.CloseNow()
	f.mu.Lock()
	f.streams = append(f.streams, r.URL.Query())
	gateway := f.gateway
	f.mu.Unlock()
	if gateway != nil {
		gateway(conn, r)
	}
}

// recorded is what the platform was asked so far.
func (f *fakePlatform) recorded() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedCall(nil), f.calls...)
}

// streamQueries are the query strings the gateway was opened with.
func (f *fakePlatform) streamQueries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.streams...)
}

func (f *fakePlatform) config() *types.MogeniusConfig {
	return &types.MogeniusConfig{
		APIKey:         "mo_pat:user:secret",
		APIUrl:         f.server.URL,
		OrganizationID: "org-1",
		ClusterID:      "cluster-1",
		StreamURL:      "ws" + strings.TrimPrefix(f.server.URL, "http"),
		HTTPClient:     f.server.Client(),
	}
}

func (f *fakePlatform) client(t *testing.T) *Client {
	t.Helper()
	client, err := NewClientWithConfig(f.config())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// sandbox is a handle as Get would return it, without a request.
func (f *fakePlatform) sandbox(t *testing.T, overrides map[string]any) *Sandbox {
	t.Helper()
	return newSandbox(f.client(t).api, decodeSandbox(t, sandboxJSON(overrides)), types.CodeLanguagePython)
}

func decodeSandbox(t *testing.T, info map[string]any) sandboxDTO {
	t.Helper()
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var dto sandboxDTO
	if err := json.Unmarshal(data, &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

// sandboxJSON is a started warm-pool sandbox as the platform returns it.
func sandboxJSON(overrides map[string]any) map[string]any {
	info := map[string]any{
		"id":            "default-abc12",
		"name":          "default-abc12",
		"namespace":     "agent-sandbox",
		"kind":          "SandboxClaim",
		"state":         "started",
		"stateReason":   nil,
		"labels":        map[string]any{},
		"profile":       "default",
		"image":         "ghcr.io/mogenius/agent-sandbox-chart/sandbox-default:1.2.0",
		"podName":       "default-k27tp",
		"sandboxName":   "default-k27tp",
		"containerName": "sandbox",
		"serviceFQDN":   "default-k27tp.agent-sandbox.svc.cluster.local",
		"operatingMode": "Running",
		"ephemeral":     true,
		"expiresAt":     nil,
		"createdAt":     "2026-10-01T12:00:00.000Z",
		"createdBy":     "jane@example.com",
	}
	for key, value := range overrides {
		info[key] = value
	}
	return info
}

// execJSON is an answer of the toolbox exec route.
func execJSON(exitCode int, stdout, stderr string) map[string]any {
	return map[string]any{
		"exitCode":   exitCode,
		"result":     stdout + stderr,
		"artifacts":  map[string]any{"stdout": stdout, "stderr": stderr},
		"truncated":  false,
		"container":  "sandbox",
		"durationMs": 12,
	}
}

// fastPolling makes the WaitFor… loops poll every millisecond for one test.
func fastPolling(t *testing.T) {
	previous := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = previous })
}

func expectCall(t *testing.T, call recordedCall, method, path string) {
	t.Helper()
	if call.method != method || call.path != path {
		t.Fatalf("got %s %s, want %s %s", call.method, call.path, method, path)
	}
}
