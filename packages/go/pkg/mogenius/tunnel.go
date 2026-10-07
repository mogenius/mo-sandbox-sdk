package mogenius

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
)

// Frames of a port-forward stream. Each TCP connection through the tunnel has
// an id: text frames open and close it, binary frames carry its bytes behind
// a one-byte id length and the id.
const (
	tunnelOpenPrefix  = "PFM:O:"
	tunnelClosePrefix = "PFM:C:"
	tunnelReady       = "PEER_IS_READY"
	tunnelReadyAck    = "ack-ready"
	tunnelPing        = "BROWSER_PING"
	tunnelPeerClosed  = "CLOSE_CONNECTION_FROM_PEER"
)

const (
	tunnelPingInterval = 5 * time.Second
	tunnelReadyTimeout = 30 * time.Second
	tunnelChunkSize    = 64 << 10
)

// tunnelReconnectDelays are the pauses before each attempt to reconnect, the
// last one repeating; tests shorten them.
var tunnelReconnectDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

var errTunnelPeerClosed = errors.New("the stream gateway closed the tunnel")

// Tunnel (mogenius) forwards TCP connections to a port of the sandbox through
// the platform's stream gateway, the way a port-forward does: whatever
// connects to Addr, or dials through DialContext, reaches that port in the
// pod. HTTP, WebSockets, gRPC and plain TCP all pass, and only bytes travel —
// the sandbox sees no credentials. When the gateway connection drops, the
// tunnel reconnects on its own; connections open at that moment break.
type Tunnel struct {
	api      *apiClient
	params   map[string]string
	listener net.Listener
	done     chan struct{}
	once     sync.Once

	mu   sync.Mutex
	conn *websocket.Conn // nil while reconnecting
	subs map[string]net.Conn
	seq  uint64
	err  error
}

// Tunnel (mogenius) opens a tunnel to port inside the sandbox and listens on
// 127.0.0.1 for connections to forward; options.WithLocalPort fixes the local
// port. ctx bounds the setup only: the tunnel lives until Close. Needs
// OrganizationID, ClusterID and the stream gateway.
func (s *Sandbox) Tunnel(ctx context.Context, port int, opts ...func(*options.Tunnel)) (*Tunnel, error) {
	if port < 1 || port > 65535 {
		return nil, sdkerrors.NewMogeniusValidationError(fmt.Sprintf("Port %d is not a TCP port.", port), nil)
	}
	podName, _, err := s.pod(ctx)
	if err != nil {
		return nil, err
	}
	tunnelOpts := &options.Tunnel{}
	for _, opt := range opts {
		opt(tunnelOpts)
	}
	t := &Tunnel{
		api: s.api,
		params: map[string]string{
			"type":         "PORT_FORWARD",
			"cmd":          "port-forward",
			"namespace":    s.Namespace,
			"kind":         "Pod",
			"workloadName": podName,
			"remotePort":   strconv.Itoa(port),
		},
		done: make(chan struct{}),
		subs: map[string]net.Conn{},
	}
	conn, err := t.connect(ctx)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tunnelOpts.LocalPort)))
	if err != nil {
		_ = conn.CloseNow()
		return nil, sdkerrors.NewMogeniusError(fmt.Sprintf("Could not listen on 127.0.0.1:%d: %v", tunnelOpts.LocalPort, err), 0, nil)
	}
	t.listener, t.conn = listener, conn
	go t.serve(conn)
	go t.accept()
	go t.ping()
	return t, nil
}

// Addr is the local address that leads into the sandbox, 127.0.0.1:<port>.
func (t *Tunnel) Addr() string {
	return t.listener.Addr().String()
}

// URL is http://Addr, for an HTTP service behind the tunnel.
func (t *Tunnel) URL() string {
	return "http://" + t.Addr()
}

// Done is closed once the tunnel has ended.
func (t *Tunnel) Done() <-chan struct{} {
	return t.done
}

// Err is why the tunnel ended on its own; nil while it runs and after Close.
func (t *Tunnel) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// DialContext opens a connection to the sandbox's port without the local
// listener — for an http.Transport, a gRPC dialer or a WebSocket client in
// the same process. network and address are ignored.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	local, remote := net.Pipe()
	if err := t.bridge(remote); err != nil {
		_ = local.Close()
		return nil, err
	}
	return local, nil
}

// Close ends the tunnel and every connection through it.
func (t *Tunnel) Close() error {
	t.shutdown(nil)
	return nil
}

func (t *Tunnel) shutdown(cause error) {
	t.once.Do(func() {
		t.mu.Lock()
		t.err = cause
		close(t.done)
		conn, subs := t.conn, t.subs
		t.conn, t.subs = nil, map[string]net.Conn{}
		t.mu.Unlock()
		_ = t.listener.Close()
		for _, sub := range subs {
			_ = sub.Close()
		}
		if conn != nil {
			// the close handshake waits for the gateway; the caller does not have to
			go func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		}
	})
}

// connect opens the gateway stream and waits until the operator holds the port.
func (t *Tunnel) connect(ctx context.Context) (*websocket.Conn, error) {
	conn, err := t.api.openStream(ctx, t.params)
	if err != nil {
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, tunnelReadyTimeout)
	defer cancel()
	for {
		typ, data, err := conn.Read(readyCtx)
		if err != nil {
			_ = conn.CloseNow()
			switch {
			case ctx.Err() != nil:
				return nil, fmt.Errorf("tunnel: %w", ctx.Err())
			case readyCtx.Err() != nil:
				return nil, sdkerrors.NewMogeniusTimeoutError(fmt.Sprintf("The cluster operator did not open the tunnel within %s.", tunnelReadyTimeout))
			case websocket.CloseStatus(err) == websocket.StatusNormalClosure && closeReason(err) == "":
				failure := sdkerrors.NewMogeniusError("The stream gateway closed the tunnel before the operator opened it.", 0, nil)
				failure.Source = sdkerrors.SourceAPI
				return nil, failure
			default:
				return nil, streamCloseError(err, t.api.cfg.streamURL)
			}
		}
		if typ != websocket.MessageText {
			continue
		}
		switch text := string(data); {
		case text == tunnelReady || text == tunnelReadyAck:
			if err := conn.Write(ctx, websocket.MessageText, []byte(tunnelReady)); err != nil {
				_ = conn.CloseNow()
				return nil, sdkerrors.NewMogeniusError(fmt.Sprintf("Answering the stream gateway failed: %v", err), 0, nil)
			}
			return conn, nil
		case strings.Contains(text, "DOES_NOT_EXIST"):
			notFound := sdkerrors.NewMogeniusNotFoundError(text, nil)
			notFound.Source = sdkerrors.SourceOperator
			_ = conn.CloseNow()
			return nil, notFound
		case strings.Contains(text, "UNAUTHORIZED"):
			forbidden := sdkerrors.NewMogeniusForbiddenError(text, nil)
			forbidden.Source = sdkerrors.SourceAPI
			_ = conn.CloseNow()
			return nil, forbidden
		case strings.Contains(text, "ERROR"):
			failure := sdkerrors.NewMogeniusError(text, 0, nil)
			failure.Source = sdkerrors.SourceOperator
			_ = conn.CloseNow()
			return nil, failure
		}
		// anything else (pings) is the gateway talking to itself
	}
}

// serve reads the gateway stream and hands each frame to its connection;
// when the stream breaks it reconnects, until the tunnel closes.
func (t *Tunnel) serve(conn *websocket.Conn) {
	for {
		err := t.read(conn)
		select {
		case <-t.done:
			return
		default:
		}
		// the connections of the broken stream are gone with it
		t.mu.Lock()
		subs := t.subs
		t.subs, t.conn = map[string]net.Conn{}, nil
		t.mu.Unlock()
		for _, sub := range subs {
			_ = sub.Close()
		}
		if errors.Is(err, errTunnelPeerClosed) {
			t.shutdown(sdkerrors.NewMogeniusError("The stream gateway closed the tunnel.", 0, nil))
			return
		}
		if conn = t.reconnect(); conn == nil {
			return
		}
	}
}

func (t *Tunnel) read(conn *websocket.Conn) error {
	for {
		typ, data, err := conn.Read(context.Background())
		if err != nil {
			return err
		}
		if typ == websocket.MessageBinary {
			if len(data) < 2 || len(data) < 1+int(data[0]) {
				continue
			}
			head := 1 + int(data[0])
			id := string(data[1:head])
			if sub := t.sub(id); sub != nil {
				if _, err := sub.Write(data[head:]); err != nil {
					t.closeSub(id, true)
				}
			}
			continue
		}
		switch text := string(data); {
		case strings.HasPrefix(text, tunnelClosePrefix):
			t.closeSub(strings.TrimPrefix(text, tunnelClosePrefix), false)
		case text == tunnelPeerClosed:
			return errTunnelPeerClosed
		}
	}
}

// reconnect dials the gateway again with growing pauses until it works, the
// pod is gone or the tunnel closes; nil means the tunnel is over.
func (t *Tunnel) reconnect() *websocket.Conn {
	for attempt := 0; ; attempt++ {
		select {
		case <-t.done:
			return nil
		case <-time.After(tunnelReconnectDelays[min(attempt, len(tunnelReconnectDelays)-1)]):
		}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			// Close must not wait for a dial in flight
			select {
			case <-t.done:
				cancel()
			case <-ctx.Done():
			}
		}()
		conn, err := t.connect(ctx)
		cancel()
		if err == nil {
			t.mu.Lock()
			select {
			case <-t.done:
				t.mu.Unlock()
				_ = conn.CloseNow()
				return nil
			default:
			}
			t.conn = conn
			t.mu.Unlock()
			return conn
		}
		var notFound *sdkerrors.MogeniusNotFoundError
		if errors.As(err, &notFound) {
			// the pod is gone; under its name it does not come back
			t.shutdown(err)
			return nil
		}
	}
}

func (t *Tunnel) accept() {
	for {
		local, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.done:
				return
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}
		if tcp, ok := local.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
		}
		// a refused connection is closed right away
		_ = t.bridge(local)
	}
}

// bridge announces a local connection to the gateway and pumps its bytes there.
func (t *Tunnel) bridge(local net.Conn) error {
	t.mu.Lock()
	conn := t.conn
	if conn == nil {
		t.mu.Unlock()
		_ = local.Close()
		select {
		case <-t.done:
			return sdkerrors.NewMogeniusError("The tunnel is closed.", 0, nil)
		default:
			return sdkerrors.NewMogeniusError("The tunnel is reconnecting to the stream gateway; try again in a moment.", 0, nil)
		}
	}
	t.seq++
	id := "c" + strconv.FormatUint(t.seq, 10)
	t.subs[id] = local
	t.mu.Unlock()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(tunnelOpenPrefix+id)); err != nil {
		t.closeSub(id, false)
		return sdkerrors.NewMogeniusError(fmt.Sprintf("Opening a connection through the tunnel failed: %v", err), 0, nil)
	}
	go t.pump(conn, id, local)
	return nil
}

// pump sends what the local side writes as binary frames: id length, id, bytes.
func (t *Tunnel) pump(conn *websocket.Conn, id string, local net.Conn) {
	defer t.closeSub(id, true)
	frame := make([]byte, 1+len(id)+tunnelChunkSize)
	frame[0] = byte(len(id))
	head := 1 + copy(frame[1:], id)
	for {
		n, err := local.Read(frame[head:])
		if n > 0 {
			if conn.Write(context.Background(), websocket.MessageBinary, frame[:head+n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// closeReason is the reason of a close frame; empty for other errors.
func closeReason(err error) string {
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return closeErr.Reason
	}
	return ""
}

func (t *Tunnel) sub(id string) net.Conn {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.subs[id]
}

// closeSub ends one connection; tell is whether the gateway still has to hear of it.
func (t *Tunnel) closeSub(id string, tell bool) {
	t.mu.Lock()
	sub, ok := t.subs[id]
	delete(t.subs, id)
	conn := t.conn
	t.mu.Unlock()
	if !ok {
		return
	}
	_ = sub.Close()
	if tell && conn != nil {
		_ = conn.Write(context.Background(), websocket.MessageText, []byte(tunnelClosePrefix+id))
	}
}

func (t *Tunnel) ping() {
	ticker := time.NewTicker(tunnelPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.mu.Lock()
			conn := t.conn
			t.mu.Unlock()
			if conn != nil {
				_ = conn.Write(context.Background(), websocket.MessageText, []byte(tunnelPing))
			}
		}
	}
}
