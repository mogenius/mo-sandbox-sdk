package mogenius

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// Frames of a gateway stream, as the operator writes them: output behind a
// one-byte stream tag, control messages as text.
const (
	streamTagStderr   = 1
	streamExitPrefix  = "EXIT:"
	streamErrorPrefix = "ERROR:"
	streamTruncated   = "TRUNCATED"
	streamTimeout     = "TIMEOUT"
	// the gateway asks for the command once the connection is authorized; it does not travel in the URL
	streamRequestPrompt = "SEND_EXEC_REQUEST"
	// far above any output frame; only guards against a runaway peer
	streamReadLimit = 64 << 20
)

// close reasons of the operator refusing a session log follow before it started
var sessionGone = regexp.MustCompile(`(?i)session not found|command not found in the session|the session has ended`)

// openStream opens a socket on the platform's stream gateway. The gateway
// reads the query string like headers, so the key and the ids travel there
// next to params. Unlike the HTTP routes, the gateway does not infer
// organization and cluster from the key, so both have to be configured.
func (a *apiClient) openStream(ctx context.Context, params map[string]string) (*websocket.Conn, error) {
	if a.cfg.organizationID == "" || a.cfg.clusterID == "" {
		return nil, sdkerrors.NewMogeniusError(
			"Streams need OrganizationID and ClusterID (MOGENIUS_ORGANIZATION_ID, MOGENIUS_CLUSTER_ID): the stream gateway does not take them from the key.",
			0, nil)
	}
	values := url.Values{}
	values.Set("authorization", "bearer "+a.cfg.apiKey)
	values.Set("organizationId", a.cfg.organizationID)
	values.Set("clusterId", a.cfg.clusterID)
	if a.cfg.workspaceName != "" {
		values.Set("workspaceName", a.cfg.workspaceName)
	}
	for key, value := range params {
		if value != "" {
			values.Set(key, value)
		}
	}

	conn, resp, err := websocket.Dial(ctx, a.cfg.streamURL+"/xterm-stream?"+values.Encode(), &websocket.DialOptions{
		HTTPClient: a.cfg.httpClient,
		HTTPHeader: http.Header{"User-Agent": {userAgent}},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("stream gateway: %w", ctx.Err())
		}
		if resp != nil && resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return nil, sdkerrors.NewMogeniusErrorFromResponse(body, resp.StatusCode, resp.Header,
				fmt.Sprintf("stream gateway → HTTP %d", resp.StatusCode))
		}
		return nil, sdkerrors.NewMogeniusError(fmt.Sprintf(
			"Could not connect to the stream gateway at %s (%v): check MOGENIUS_STREAM_URL, the key, and that OrganizationID and ClusterID are set and allow commands in this pod.",
			a.cfg.streamURL, err), 0, nil)
	}
	conn.SetReadLimit(streamReadLimit)
	return conn, nil
}

// streamEvents reads one gateway stream into events: output frames by their
// stream tag, control frames into the final exit event or an error. request,
// when not nil, is sent as the gateway asks for it (an exec stream); a log
// follow has nothing to send. It yields until the exit event, an error or
// the consumer stops, and closes the connection before it returns: closing
// a stream the peer has not closed stops the command in the container.
func streamEvents(ctx context.Context, conn *websocket.Conn, request []byte, streamURL string, yield func(types.ExecEvent, error) bool) {
	var (
		exitCode  *int
		truncated bool
		timedOut  bool
		peerDone  bool
	)
	defer func() {
		if peerDone {
			_ = conn.CloseNow()
			return
		}
		// the close handshake waits for the peer; the consumer does not have to
		go func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	}()

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			peerDone = true
			switch {
			case exitCode != nil:
				yield(types.ExecEvent{Type: types.ExecEventExit, ExitCode: *exitCode, Truncated: truncated, TimedOut: timedOut}, nil)
			case ctx.Err() != nil:
				yield(types.ExecEvent{}, fmt.Errorf("stream gateway: %w", ctx.Err()))
			default:
				yield(types.ExecEvent{}, streamCloseError(err, streamURL))
			}
			return
		}

		if typ == websocket.MessageBinary {
			if len(data) > 1 {
				event := types.ExecEvent{Type: types.ExecEventStdout, Data: data[1:]}
				if data[0] == streamTagStderr {
					event.Type = types.ExecEventStderr
				}
				if !yield(event, nil) {
					return
				}
			}
			continue
		}

		text := string(data)
		switch {
		case text == streamRequestPrompt:
			if request != nil {
				if err := conn.Write(ctx, websocket.MessageText, request); err != nil {
					peerDone = true
					yield(types.ExecEvent{}, sdkerrors.NewMogeniusError(fmt.Sprintf("Sending the command to the stream gateway failed: %v", err), 0, nil))
					return
				}
			}
		case strings.HasPrefix(text, streamExitPrefix):
			if code, err := strconv.Atoi(strings.TrimSpace(text[len(streamExitPrefix):])); err == nil {
				exitCode = &code
			}
		case text == streamTruncated:
			truncated = true
		case text == streamTimeout:
			timedOut = true
		case strings.HasPrefix(text, streamErrorPrefix):
			failure := sdkerrors.NewMogeniusError(text[len(streamErrorPrefix):], 0, nil)
			failure.Source = sdkerrors.SourceOperator
			yield(types.ExecEvent{}, failure)
			return
		}
		// anything else (PEER_IS_READY, pings) is the gateway talking to itself
	}
}

// streamCloseError is the error a stream that ended without an exit code stands for.
func streamCloseError(err error, streamURL string) error {
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		return sdkerrors.NewMogeniusError(fmt.Sprintf(
			"The connection to the stream gateway at %s broke before the command reported an exit code: %v", streamURL, err), 0, nil)
	}
	reason := closeErr.Reason
	switch {
	case reason == "POD_DOES_NOT_EXIST":
		notFound := sdkerrors.NewMogeniusNotFoundError("The sandbox pod does not exist (any more).", nil)
		notFound.Source = sdkerrors.SourceOperator
		return notFound
	case reason == "OPERATOR_NOT_CONNECTED":
		timeout := sdkerrors.NewMogeniusTimeoutError(
			"The cluster operator did not connect to the stream gateway. It dials the address the platform gives it (MO_PRODUCT_NEST__K8S_CMD_STREAM_WEBSOCKET_HOST), which must be reachable from where the operator runs.")
		timeout.Source = sdkerrors.SourceAPI
		timeout.ErrorCode = "OPERATOR_TIMEOUT"
		return timeout
	case sessionGone.MatchString(reason):
		// the operator refused the request before the stream opened
		notFound := sdkerrors.NewMogeniusNotFoundError(reason, nil)
		notFound.Source = sdkerrors.SourceOperator
		return notFound
	}
	message := reason
	if closeErr.Code == websocket.StatusNormalClosure {
		message = "The stream closed before the command reported an exit code."
	} else if message == "" {
		message = fmt.Sprintf("The stream closed with code %d.", closeErr.Code)
	}
	failure := sdkerrors.NewMogeniusError(message, 0, nil)
	failure.Source = sdkerrors.SourceAPI
	return failure
}
