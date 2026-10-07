package mogenius

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
)

// echoGateway plays the port-forward side of the stream gateway: it opens
// with PEER_IS_READY, echoes each connection's bytes back upper-cased and
// reports every control frame, and how the stream ended, on events.
func echoGateway(events chan<- string) func(*websocket.Conn, *http.Request) {
	return func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("PEER_IS_READY"))
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				events <- "closed:" + strconv.Itoa(int(websocket.CloseStatus(err)))
				return
			}
			if typ == websocket.MessageText {
				if text := string(data); text != "BROWSER_PING" {
					events <- text
				}
				continue
			}
			head := 1 + int(data[0])
			reply := append(append([]byte{}, data[:head]...), bytes.ToUpper(data[head:])...)
			_ = conn.Write(ctx, websocket.MessageBinary, reply)
		}
	}
}

func expectEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("gateway saw %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("gateway never saw %q", want)
	}
}

func TestTunnelForwardsTCP(t *testing.T) {
	f := newFakePlatform(t)
	events := make(chan string, 100)
	f.gateway = echoGateway(events)

	tunnel, err := f.sandbox(t, nil).Tunnel(context.Background(), 8080)
	if err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "PEER_IS_READY")

	conn, err := net.Dial("tcp", tunnel.Addr())
	if err != nil {
		t.Fatal(err)
	}
	expectEvent(t, events, "PFM:O:c1")
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 5)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "HELLO" {
		t.Fatalf("reply %q, %v", reply, err)
	}
	_ = conn.Close()
	expectEvent(t, events, "PFM:C:c1")

	query := f.streamQueries()[0]
	for key, value := range map[string]string{
		"authorization": "bearer mo_pat:user:secret", "organizationId": "org-1", "clusterId": "cluster-1",
		"type": "PORT_FORWARD", "cmd": "port-forward", "namespace": "agent-sandbox", "kind": "Pod",
		"workloadName": "default-k27tp", "remotePort": "8080",
	} {
		if query.Get(key) != value {
			t.Errorf("query %s = %q, want %q", key, query.Get(key), value)
		}
	}
	if query.Has("binary") || !strings.HasPrefix(tunnel.URL(), "http://127.0.0.1:") {
		t.Fatalf("query %v, url %s", query, tunnel.URL())
	}

	_ = tunnel.Close()
	expectEvent(t, events, "closed:1000")
	<-tunnel.Done()
	if tunnel.Err() != nil {
		t.Fatalf("Err after Close: %v", tunnel.Err())
	}
	if _, err := net.Dial("tcp", tunnel.Addr()); err == nil {
		t.Fatal("the listener is still open")
	}
}

func TestTunnelCarriesHTTPThroughDialContext(t *testing.T) {
	f := newFakePlatform(t)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("PEER_IS_READY"))
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageBinary {
				continue
			}
			// an HTTP server in the pod answering whatever arrives
			head := 1 + int(data[0])
			response := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"
			_ = conn.Write(ctx, websocket.MessageBinary, append(append([]byte{}, data[:head]...), response...))
			_ = conn.Write(ctx, websocket.MessageText, []byte("PFM:C:"+string(data[1:head])))
		}
	}
	tunnel, err := f.sandbox(t, nil).Tunnel(context.Background(), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()

	client := &http.Client{Transport: &http.Transport{DialContext: tunnel.DialContext}}
	response, err := client.Get("http://sandbox/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("%d %q", response.StatusCode, body)
	}
}

func TestTunnelConnectionClosedByThePod(t *testing.T) {
	f := newFakePlatform(t)
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte("PEER_IS_READY"))
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if id, ok := strings.CutPrefix(string(data), "PFM:O:"); ok {
				_ = conn.Write(ctx, websocket.MessageText, []byte("PFM:C:"+id))
			}
		}
	}
	tunnel, err := f.sandbox(t, nil).Tunnel(context.Background(), 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()

	conn, err := net.Dial("tcp", tunnel.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
}

func TestTunnelReportsWhyItCannotOpen(t *testing.T) {
	cases := []struct {
		gateway func(*websocket.Conn, *http.Request)
		check   func(error) bool
	}{
		{func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Close(websocket.StatusPolicyViolation, "POD_DOES_NOT_EXIST")
		}, func(err error) bool {
			var notFound *sdkerrors.MogeniusNotFoundError
			return errors.As(err, &notFound)
		}},
		{func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("ERROR: port 8080 is not open"))
			_, _, _ = conn.Read(r.Context())
		}, func(err error) bool {
			var base *sdkerrors.MogeniusError
			return errors.As(err, &base) && base.Source == sdkerrors.SourceOperator && strings.Contains(base.Message, "8080")
		}},
		{func(conn *websocket.Conn, r *http.Request) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("UNAUTHORIZED"))
			_, _, _ = conn.Read(r.Context())
		}, func(err error) bool {
			var forbidden *sdkerrors.MogeniusForbiddenError
			return errors.As(err, &forbidden)
		}},
	}
	for i, c := range cases {
		f := newFakePlatform(t)
		f.gateway = c.gateway
		if _, err := f.sandbox(t, nil).Tunnel(context.Background(), 8080); !c.check(err) {
			t.Errorf("case %d: got %v", i, err)
		}
	}
}

func TestTunnelReconnectsWhenTheStreamBreaks(t *testing.T) {
	previous := tunnelReconnectDelays
	tunnelReconnectDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { tunnelReconnectDelays = previous })

	f := newFakePlatform(t)
	events := make(chan string, 100)
	echo := echoGateway(events)
	var streams atomic.Int32
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		if streams.Add(1) == 1 {
			// the first stream breaks once a connection is open
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("PEER_IS_READY"))
			_, _, _ = conn.Read(r.Context())
			_, _, _ = conn.Read(r.Context())
			_ = conn.Close(websocket.StatusInternalError, "operator restarted")
			return
		}
		echo(conn, r)
	}
	tunnel, err := f.sandbox(t, nil).Tunnel(context.Background(), 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()

	first, err := net.Dial("tcp", tunnel.Addr())
	if err != nil {
		t.Fatal(err)
	}
	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil {
		t.Fatal("a connection of the broken stream must end")
	}
	expectEvent(t, events, "PEER_IS_READY")

	// the tunnel takes the new stream a moment after the gateway answered
	var reply string
	for attempt := 0; attempt < 50 && reply != "AGAIN"; attempt++ {
		reply = echoOnce(t, tunnel.Addr(), "again")
	}
	if reply != "AGAIN" {
		t.Fatalf("reply %q after reconnecting", reply)
	}
	if len(f.streamQueries()) != 2 {
		t.Fatalf("%d streams", len(f.streamQueries()))
	}
}

func TestTunnelEndsWhenThePodIsGone(t *testing.T) {
	previous := tunnelReconnectDelays
	tunnelReconnectDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { tunnelReconnectDelays = previous })

	f := newFakePlatform(t)
	var streams atomic.Int32
	f.gateway = func(conn *websocket.Conn, r *http.Request) {
		if streams.Add(1) == 1 {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte("PEER_IS_READY"))
			_, _, _ = conn.Read(r.Context())
			_ = conn.Close(websocket.StatusInternalError, "")
			return
		}
		_ = conn.Close(websocket.StatusPolicyViolation, "POD_DOES_NOT_EXIST")
	}
	tunnel, err := f.sandbox(t, nil).Tunnel(context.Background(), 8080)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-tunnel.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the tunnel did not end")
	}
	var notFound *sdkerrors.MogeniusNotFoundError
	if !errors.As(tunnel.Err(), &notFound) {
		t.Fatalf("Err %v", tunnel.Err())
	}
}

func TestTunnelNeedsAPodAndAPort(t *testing.T) {
	f := newFakePlatform(t)
	var validation *sdkerrors.MogeniusValidationError
	if _, err := f.sandbox(t, nil).Tunnel(context.Background(), 0); !errors.As(err, &validation) {
		t.Fatalf("port 0: %v", err)
	}
	var conflict *sdkerrors.MogeniusConflictError
	if _, err := f.sandbox(t, map[string]any{"podName": nil}).Tunnel(context.Background(), 8080); !errors.As(err, &conflict) {
		t.Fatalf("no pod: %v", err)
	}
	if len(f.streamQueries()) != 0 {
		t.Fatal("nothing should have been opened")
	}
}

// echoOnce sends text through a fresh connection and returns what came back, empty when nothing did.
func echoOnce(t *testing.T, addr, text string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := conn.Write([]byte(text)); err != nil {
		return ""
	}
	reply := make([]byte, len(text))
	if _, err := io.ReadFull(conn, reply); err != nil {
		return ""
	}
	return string(reply)
}

func TestTunnelReadsTheSandboxAgainWithoutAPod(t *testing.T) {
	f := newFakePlatform(t, reply{body: sandboxJSON(nil)})
	events := make(chan string, 100)
	f.gateway = echoGateway(events)

	// created without waiting: the handle has no pod yet, the platform has one by now
	tunnel, err := f.sandbox(t, map[string]any{"podName": nil, "state": "starting"}).Tunnel(context.Background(), 8080)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()

	expectCall(t, f.recorded()[0], http.MethodGet, "/sandbox/agent-sandbox/default-abc12")
	if pod := f.streamQueries()[0].Get("workloadName"); pod != "default-k27tp" {
		t.Fatalf("workloadName %q", pod)
	}
}
