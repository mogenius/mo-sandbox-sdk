package mogenius

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func frame(tag byte, text string) []byte {
	return append([]byte{tag}, text...)
}

func TestExecuteCommandStream(t *testing.T) {
	f := newFakePlatform(t)
	requests := make(chan string, 1)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("PEER_IS_READY"))
		_ = conn.Write(ctx, websocket.MessageText, []byte("SEND_EXEC_REQUEST"))
		_, request, err := conn.Read(ctx)
		if err != nil {
			t.Errorf("gateway read: %v", err)
			return
		}
		requests <- string(request)
		_ = conn.Write(ctx, websocket.MessageBinary, frame(0, "out\n"))
		_ = conn.Write(ctx, websocket.MessageBinary, frame(1, "err\n"))
		_ = conn.Write(ctx, websocket.MessageText, []byte("TRUNCATED"))
		_ = conn.Write(ctx, websocket.MessageText, []byte("EXIT:3"))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}

	var events []types.ExecEvent
	for event, err := range f.sandbox(t, nil).Process.ExecuteCommandStream(context.Background(), "make test",
		options.WithExecuteTimeout(30*time.Second)) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}

	want := []types.ExecEvent{
		{Type: types.ExecEventStdout, Data: []byte("out\n")},
		{Type: types.ExecEventStderr, Data: []byte("err\n")},
		{Type: types.ExecEventExit, ExitCode: 3, Truncated: true},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events %+v", events)
	}
	if request := <-requests; request != `{"command":"make test","timeout":30}` {
		t.Fatalf("request %s", request)
	}
	query := f.streamQueries()[0]
	for key, value := range map[string]string{
		"authorization": "bearer mo_pat:user:secret", "organizationId": "org-1", "clusterId": "cluster-1",
		"type": "CLUSTER__POD_EXEC", "cmd": "exec", "namespace": "agent-sandbox", "podName": "default-k27tp",
		"container": "sandbox", "binary": "1",
	} {
		if query.Get(key) != value {
			t.Errorf("query %s = %q, want %q", key, query.Get(key), value)
		}
	}
	// the command never travels in the URL
	if strings.Contains(query.Encode(), "make") {
		t.Fatalf("query %v", query)
	}
}

func TestExecuteCommandStreamCloseReasons(t *testing.T) {
	cases := []struct {
		code   websocket.StatusCode
		reason string
		check  func(error) bool
	}{
		{websocket.StatusPolicyViolation, "POD_DOES_NOT_EXIST", func(err error) bool {
			var notFound *sdkerrors.MogeniusNotFoundError
			return errors.As(err, &notFound) && notFound.Source == sdkerrors.SourceOperator
		}},
		{websocket.StatusPolicyViolation, "OPERATOR_NOT_CONNECTED", func(err error) bool {
			var timeout *sdkerrors.MogeniusTimeoutError
			return errors.As(err, &timeout) && timeout.ErrorCode == "OPERATOR_TIMEOUT"
		}},
		{websocket.StatusPolicyViolation, "session not found: build", func(err error) bool {
			var notFound *sdkerrors.MogeniusNotFoundError
			return errors.As(err, &notFound)
		}},
		{websocket.StatusNormalClosure, "", func(err error) bool {
			return strings.Contains(err.Error(), "before the command reported an exit code")
		}},
		{websocket.StatusInternalError, "", func(err error) bool {
			return strings.Contains(err.Error(), "closed with code")
		}},
	}
	for _, c := range cases {
		f := newFakePlatform(t)
		f.gateway = func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Close(c.code, c.reason)
		}
		var got error
		for _, err := range f.sandbox(t, nil).Process.ExecuteCommandStream(context.Background(), "ls") {
			got = err
		}
		if got == nil || !c.check(got) {
			t.Errorf("%d %q: got %v", c.code, c.reason, got)
		}
	}
}

func TestExecuteCommandStreamErrorFrame(t *testing.T) {
	f := newFakePlatform(t)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		_ = conn.Write(r.Context(), websocket.MessageText, []byte("ERROR:container sandbox is not running"))
		_, _, _ = conn.Read(r.Context())
	}

	var got error
	for _, err := range f.sandbox(t, nil).Process.ExecuteCommandStream(context.Background(), "ls") {
		got = err
	}
	var base *sdkerrors.MogeniusError
	if !errors.As(got, &base) || base.Source != sdkerrors.SourceOperator || base.Message != "container sandbox is not running" {
		t.Fatalf("got %v", got)
	}
}

func TestLeavingTheStreamEarlyClosesIt(t *testing.T) {
	f := newFakePlatform(t)
	closed := make(chan websocket.StatusCode, 1)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("SEND_EXEC_REQUEST"))
		_, _, _ = conn.Read(ctx)
		_ = conn.Write(ctx, websocket.MessageBinary, frame(0, "started\n"))
		// blocks until the client closes the stream
		_, _, err := conn.Read(ctx)
		closed <- websocket.CloseStatus(err)
	}

	for event, err := range f.sandbox(t, nil).Process.ExecuteCommandStream(context.Background(), "sleep 60") {
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == types.ExecEventStdout {
			break
		}
	}

	select {
	case code := <-closed:
		if code != websocket.StatusNormalClosure {
			t.Fatalf("closed with %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream was not closed")
	}
}

func TestStreamsNeedOrganizationAndCluster(t *testing.T) {
	f := newFakePlatform(t)
	config := f.config()
	config.OrganizationID = ""
	client, err := NewClientWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox := newSandbox(client.api, decodeSandbox(t, sandboxJSON(nil)), types.CodeLanguagePython)

	var got error
	for _, err := range sandbox.Process.ExecuteCommandStream(context.Background(), "ls") {
		got = err
	}
	if got == nil || !strings.Contains(got.Error(), "OrganizationID and ClusterID") || len(f.streamQueries()) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestStreamNeedsAPod(t *testing.T) {
	f := newFakePlatform(t)
	var got error
	for _, err := range f.sandbox(t, map[string]any{"podName": nil}).Process.ExecuteCommandStream(context.Background(), "ls") {
		got = err
	}
	var conflict *sdkerrors.MogeniusConflictError
	if !errors.As(got, &conflict) {
		t.Fatalf("got %v", got)
	}
}

func TestGetSessionCommandLogsStream(t *testing.T) {
	f := newFakePlatform(t)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		// "€" is split across two frames
		_ = conn.Write(ctx, websocket.MessageBinary, frame(0, "price: \xe2\x82"))
		_ = conn.Write(ctx, websocket.MessageBinary, frame(0, "\xac\n"))
		_ = conn.Write(ctx, websocket.MessageBinary, frame(1, "warn\n"))
		_ = conn.Write(ctx, websocket.MessageText, []byte("EXIT:0"))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	stdout := make(chan string, 10)
	stderr := make(chan string, 10)

	if err := f.sandbox(t, nil).Process.GetSessionCommandLogsStream(context.Background(), "build", "c1", stdout, stderr); err != nil {
		t.Fatal(err)
	}

	var out, errOut []string
	for chunk := range stdout {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %q cuts a character", chunk)
		}
		out = append(out, chunk)
	}
	for chunk := range stderr {
		errOut = append(errOut, chunk)
	}
	if strings.Join(out, "") != "price: €\n" || !reflect.DeepEqual(errOut, []string{"warn\n"}) {
		t.Fatalf("stdout %q, stderr %q", out, errOut)
	}
	query := f.streamQueries()[0]
	if query.Get("type") != "CLUSTER__POD_SESSION_LOG" || query.Get("cmd") != "session-log" ||
		query.Get("sessionId") != "build" || query.Get("cmdId") != "c1" || query.Get("podName") != "default-k27tp" {
		t.Fatalf("query %v", query)
	}
}

func TestGetSessionCommandLogsStreamClosesBothChannelsOnError(t *testing.T) {
	f := newFakePlatform(t)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		_ = conn.Close(websocket.StatusPolicyViolation, "the session has ended")
	}
	output := make(chan string, 1)

	// one channel for both streams is closed once
	err := f.sandbox(t, nil).Process.GetSessionCommandLogsStream(context.Background(), "build", "c1", output, output)

	var notFound *sdkerrors.MogeniusNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("got %v", err)
	}
	if _, open := <-output; open {
		t.Fatal("the channel is still open")
	}
}

func TestExecuteCommandStreamInAnotherContainer(t *testing.T) {
	f := newFakePlatform(t)
	requests := make(chan string, 1)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("SEND_EXEC_REQUEST"))
		_, request, _ := conn.Read(ctx)
		requests <- string(request)
		_ = conn.Write(ctx, websocket.MessageText, []byte("EXIT:0"))
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}

	for _, err := range f.sandbox(t, nil).Process.ExecuteCommandStream(context.Background(), "ls", options.WithContainer("sidecar")) {
		if err != nil {
			t.Fatal(err)
		}
	}

	if container := f.streamQueries()[0].Get("container"); container != "sidecar" {
		t.Fatalf("query container %q", container)
	}
	if request := <-requests; !strings.Contains(request, `"container":"sidecar"`) {
		t.Fatalf("request %s", request)
	}
}
