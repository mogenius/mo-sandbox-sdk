package mogenius

import (
	"context"
	"encoding/json"
	"iter"
	"time"
	"unicode/utf8"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// DefaultCommandTimeout applies when a call names no timeout.
const DefaultCommandTimeout = 10 * time.Second

// ProcessService runs commands, code and sessions in a sandbox. Everything
// goes through the platform API to the operator, which executes inside the
// pod without a TTY — no agent in the image, no open port.
type ProcessService struct {
	sandbox *Sandbox
}

// executeResponseBody is what the platform answers to the toolbox exec route.
type executeResponseBody struct {
	ExitCode  int    `json:"exitCode"`
	Result    string `json:"result"`
	Artifacts *struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	} `json:"artifacts"`
}

// execRequest is the body of the toolbox exec route, and the first frame of an exec stream.
func execRequest(command string, opts *options.ExecuteCommand) map[string]any {
	body := map[string]any{"command": command}
	if opts.Cwd != nil && *opts.Cwd != "" {
		body["cwd"] = *opts.Cwd
	}
	if len(opts.Env) > 0 {
		body["env"] = opts.Env
	}
	timeout := DefaultCommandTimeout
	if opts.Timeout != nil {
		timeout = *opts.Timeout
	}
	if timeout > 0 {
		body["timeout"] = ceilSeconds(timeout)
	}
	return body
}

// ExecuteCommand runs a shell command line. Pipes, && and quoting behave as
// in the image's shell. Stateless: cd and export do not carry over to the
// next call — pass WithCwd and WithCommandEnv instead. A non-zero exit code
// is a result, not an error.
func (p *ProcessService) ExecuteCommand(ctx context.Context, command string, opts ...func(*options.ExecuteCommand)) (*types.ExecuteResponse, error) {
	execOpts := &options.ExecuteCommand{}
	for _, opt := range opts {
		opt(execOpts)
	}
	var body executeResponseBody
	if err := p.sandbox.api.post(ctx, p.sandbox.path()+"/toolbox/process/execute", nil, execRequest(command, execOpts), &body); err != nil {
		return nil, err
	}
	artifacts := &types.ExecutionArtifacts{}
	if body.Artifacts != nil {
		artifacts.Stdout, artifacts.Stderr = body.Artifacts.Stdout, body.Artifacts.Stderr
	}
	return &types.ExecuteResponse{ExitCode: body.ExitCode, Result: body.Result, Artifacts: artifacts}, nil
}

// CodeRun runs a snippet of code with the sandbox's default language (or
// WithCodeRunLanguage): Python through python3, TypeScript through tsx,
// JavaScript through node. The code travels base64-encoded, so quotes and
// newlines need no escaping. Charts a Python run prints come back in
// Artifacts.Charts.
func (p *ProcessService) CodeRun(ctx context.Context, code string, opts ...func(*options.CodeRun)) (*types.ExecuteResponse, error) {
	runOpts := &options.CodeRun{}
	for _, opt := range opts {
		opt(runOpts)
	}
	language := runOpts.Language
	if language == "" {
		language = p.sandbox.language
	}
	params := types.CodeRunParams{}
	if runOpts.Params != nil {
		params = *runOpts.Params
	}
	command, err := buildCodeRunCommand(code, language, params.Argv)
	if err != nil {
		return nil, err
	}
	execOpts := []func(*options.ExecuteCommand){options.WithCommandEnv(params.Env)}
	if runOpts.Timeout != nil {
		execOpts = append(execOpts, options.WithExecuteTimeout(*runOpts.Timeout))
	}
	response, err := p.ExecuteCommand(ctx, command, execOpts...)
	if err != nil || language != types.CodeLanguagePython {
		return response, err
	}
	text, charts := extractCharts(response.Artifacts.Stdout)
	stderr := response.Artifacts.Stderr
	return &types.ExecuteResponse{
		ExitCode:  response.ExitCode,
		Result:    text + stderr,
		Artifacts: &types.ExecutionArtifacts{Stdout: text, Stderr: stderr, Charts: charts},
	}, nil
}

// ExecuteCommandStream (mogenius) runs a command like ExecuteCommand but
// delivers its output while it runs — for builds, tests and servers:
//
//	for event, err := range sandbox.Process.ExecuteCommandStream(ctx, "make test") {
//		if err != nil {
//			return err
//		}
//		switch event.Type {
//		case types.ExecEventStdout, types.ExecEventStderr:
//			os.Stdout.Write(event.Data)
//		case types.ExecEventExit:
//			fmt.Println("exit", event.ExitCode)
//		}
//	}
//
// stdout and stderr chunks come as they arrive, the last event is the exit.
// Leaving the loop early closes the stream, which stops the command in the
// container within seconds. The command is stateless and gets no stdin,
// like ExecuteCommand. Needs OrganizationID, ClusterID and the stream gateway.
func (p *ProcessService) ExecuteCommandStream(ctx context.Context, command string, opts ...func(*options.ExecuteCommand)) iter.Seq2[types.ExecEvent, error] {
	return func(yield func(types.ExecEvent, error) bool) {
		execOpts := &options.ExecuteCommand{}
		for _, opt := range opts {
			opt(execOpts)
		}
		podName := p.sandbox.podName()
		if podName == "" {
			yield(types.ExecEvent{}, sdkConflict("The sandbox has no pod yet: wait until it is started before streaming a command."))
			return
		}
		// the same body as the toolbox route, sent once the gateway asks for it
		request, err := json.Marshal(execRequest(command, execOpts))
		if err != nil {
			yield(types.ExecEvent{}, sdkerrors.NewMogeniusError("The command is not JSON: "+err.Error(), 0, nil))
			return
		}
		conn, err := p.sandbox.api.openStream(ctx, map[string]string{
			"type":      "CLUSTER__POD_EXEC",
			"cmd":       "exec",
			"namespace": p.sandbox.Namespace,
			"podName":   podName,
			"container": p.sandbox.ContainerName,
		})
		if err != nil {
			yield(types.ExecEvent{}, err)
			return
		}
		streamEvents(ctx, conn, request, p.sandbox.api.cfg.streamURL, yield)
	}
}

/*********************************************************************************************************************
 * sessions
 ********************************************************************************************************************/

// What the platform answers on the session routes.
type sessionBody struct {
	SessionID  string               `json:"sessionId"`
	Container  string               `json:"container"`
	CreatedAt  string               `json:"createdAt"`
	LastUsedAt string               `json:"lastUsedAt"`
	Commands   []sessionCommandBody `json:"commands"`
}

type sessionCommandBody struct {
	ID       string `json:"id"`
	Command  string `json:"command"`
	ExitCode *int32 `json:"exitCode"`
}

type sessionExecuteBody struct {
	CmdID    string  `json:"cmdId"`
	ExitCode *int32  `json:"exitCode"`
	Output   *string `json:"output"`
}

// CreateSession opens a session: a shell of its own in the sandbox where
// state carries over from command to command (cd, export, a virtualenv).
// Commands run in it one at a time. The session lives until DeleteSession,
// until it has idled for the cluster's timeout (30 minutes by default), or
// until the sandbox stops. sessionID is a name of your choice, unique within
// the sandbox.
func (p *ProcessService) CreateSession(ctx context.Context, sessionID string) error {
	path, err := p.sessionBase()
	if err != nil {
		return err
	}
	body := map[string]any{"sessionId": sessionID}
	// the pod route would take the pod's first container; a sandbox means its own
	if p.sandbox.ContainerName != "" {
		body["container"] = p.sandbox.ContainerName
	}
	return p.sandbox.api.post(ctx, path, nil, body, nil)
}

// GetSession returns the session: sessionId and commands (each with id,
// command and, once ended, exitCode), plus mogenius' container, createdAt
// and lastUsedAt.
func (p *ProcessService) GetSession(ctx context.Context, sessionID string) (map[string]any, error) {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	var body sessionBody
	if err := p.sandbox.api.get(ctx, path, nil, &body); err != nil {
		return nil, err
	}
	return sessionMap(body), nil
}

// ListSessions returns the sessions of this sandbox that your key opened and
// that are still running, shaped like GetSession.
func (p *ProcessService) ListSessions(ctx context.Context) ([]map[string]any, error) {
	path, err := p.sessionBase()
	if err != nil {
		return nil, err
	}
	var bodies []sessionBody
	if err := p.sandbox.api.get(ctx, path, nil, &bodies); err != nil {
		return nil, err
	}
	sessions := make([]map[string]any, len(bodies))
	for i, body := range bodies {
		sessions[i] = sessionMap(body)
	}
	return sessions, nil
}

// DeleteSession ends the session's shell. A command still running in it is stopped with it.
func (p *ProcessService) DeleteSession(ctx context.Context, sessionID string) error {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return err
	}
	return p.sandbox.api.delete(ctx, path, nil, nil)
}

// ExecuteSessionCommand runs a command in the session. Without runAsync it
// waits up to 60 s and answers with id, exitCode and the output; a command
// still running then is not stopped — the answer has no exitCode and the
// result is fetched later, as after runAsync, which returns right away with
// the id. stdout carries stdout and stderr in arrival order (the session's
// shell writes both into one stream); output is the same under the name the
// platform uses. suppressInputEcho is accepted for compatibility: input is
// never echoed. An exit in the command ends the shell and with it the session.
func (p *ProcessService) ExecuteSessionCommand(ctx context.Context, sessionID, command string, runAsync bool, suppressInputEcho bool) (map[string]any, error) {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	request := map[string]any{"command": command}
	if runAsync {
		request["runAsync"] = true
	}
	var body sessionExecuteBody
	if err := p.sandbox.api.post(ctx, path+"/exec", nil, request, &body); err != nil {
		return nil, err
	}
	result := map[string]any{"id": body.CmdID}
	if body.ExitCode != nil {
		result["exitCode"] = *body.ExitCode
	}
	if body.Output != nil {
		result["stdout"] = *body.Output
		result["output"] = *body.Output
	}
	return result, nil
}

// GetSessionCommand returns id, command and, once it has ended, exitCode.
func (p *ProcessService) GetSessionCommand(ctx context.Context, sessionID, commandID string) (map[string]any, error) {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	var body sessionCommandBody
	if err := p.sandbox.api.get(ctx, path+"/command/"+segment(commandID), nil, &body); err != nil {
		return nil, err
	}
	return commandMap(body), nil
}

// GetSessionCommandLogs returns what the command has printed so far.
func (p *ProcessService) GetSessionCommandLogs(ctx context.Context, sessionID, commandID string) (*types.SessionCommandLogsResponse, error) {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	var body struct {
		Output string `json:"output"`
	}
	if err := p.sandbox.api.get(ctx, path+"/command/"+segment(commandID)+"/logs", nil, &body); err != nil {
		return nil, err
	}
	return &types.SessionCommandLogsResponse{Output: body.Output}, nil
}

// GetSessionCommandLogsStream follows the command live over the stream
// gateway — what it printed first, then every new chunk as it comes — and
// returns when the command has ended. stdout and stderr receive the chunks of
// their stream, whole characters only; both are closed when it returns.
// Needs OrganizationID, ClusterID and the stream gateway.
func (p *ProcessService) GetSessionCommandLogsStream(ctx context.Context, sessionID, commandID string, stdout, stderr chan<- string) error {
	defer func() {
		close(stdout)
		if stderr != stdout {
			close(stderr)
		}
	}()
	podName := p.sandbox.podName()
	if podName == "" {
		return sdkConflict("The sandbox has no pod: there is no session to follow.")
	}
	conn, err := p.sandbox.api.openStream(ctx, map[string]string{
		"type":      "CLUSTER__POD_SESSION_LOG",
		"cmd":       "session-log",
		"namespace": p.sandbox.Namespace,
		"podName":   podName,
		"sessionId": sessionID,
		"cmdId":     commandID,
	})
	if err != nil {
		return err
	}

	var result error
	var outRest, errRest []byte
	send := func(target chan<- string, rest *[]byte, data []byte, final bool) bool {
		chunk := append(*rest, data...)
		*rest = nil
		if !final {
			chunk, *rest = splitUTF8(chunk)
		}
		if len(chunk) == 0 {
			return true
		}
		select {
		case target <- string(chunk):
			return true
		case <-ctx.Done():
			result = ctx.Err()
			return false
		}
	}
	streamEvents(ctx, conn, nil, p.sandbox.api.cfg.streamURL, func(event types.ExecEvent, err error) bool {
		switch {
		case err != nil:
			result = err
			return false
		case event.Type == types.ExecEventStdout:
			return send(stdout, &outRest, event.Data, false)
		case event.Type == types.ExecEventStderr:
			return send(stderr, &errRest, event.Data, false)
		default:
			return send(stdout, &outRest, nil, true) && send(stderr, &errRest, nil, true)
		}
	})
	return result
}

// SendSessionCommandInput writes to the standard input of the command running
// in the session — what read or an interactive program is waiting for.
// Written verbatim: end a line with \n.
func (p *ProcessService) SendSessionCommandInput(ctx context.Context, sessionID, commandID, data string) error {
	path, err := p.sessionPath(sessionID)
	if err != nil {
		return err
	}
	return p.sandbox.api.post(ctx, path+"/command/"+segment(commandID)+"/input", nil, map[string]any{"data": data}, nil)
}

// sessionBase: sessions belong to the pod, not to the sandbox object, so the
// platform serves them on its pod routes.
func (p *ProcessService) sessionBase() (string, error) {
	podName := p.sandbox.podName()
	if podName == "" {
		return "", sdkConflict("The sandbox has no pod yet: wait until it is started before using sessions.")
	}
	return "/resource/session/" + segment(p.sandbox.Namespace) + "/" + segment(podName), nil
}

func (p *ProcessService) sessionPath(sessionID string) (string, error) {
	if sessionID == "" {
		return "", sdkerrors.NewMogeniusValidationError("A session id is required.", nil)
	}
	base, err := p.sessionBase()
	if err != nil {
		return "", err
	}
	return base + "/" + segment(sessionID), nil
}

func sessionMap(body sessionBody) map[string]any {
	commands := make([]map[string]any, len(body.Commands))
	for i, command := range body.Commands {
		commands[i] = commandMap(command)
	}
	return map[string]any{
		"sessionId":  body.SessionID,
		"commands":   commands,
		"container":  body.Container,
		"createdAt":  body.CreatedAt,
		"lastUsedAt": body.LastUsedAt,
	}
}

func commandMap(body sessionCommandBody) map[string]any {
	command := map[string]any{"id": body.ID, "command": body.Command}
	if body.ExitCode != nil {
		command["exitCode"] = *body.ExitCode
	}
	return command
}

// sdkConflict is a conflict the SDK decided itself, not an HTTP answer.
func sdkConflict(message string) *sdkerrors.MogeniusConflictError {
	conflict := sdkerrors.NewMogeniusConflictError(message, nil)
	conflict.StatusCode = 0
	return conflict
}

// splitUTF8 holds back a character cut off at the end of a chunk, so it goes
// out whole with the next one.
func splitUTF8(chunk []byte) ([]byte, []byte) {
	for i := len(chunk) - 1; i >= 0 && i >= len(chunk)-utf8.UTFMax; i-- {
		if utf8.RuneStart(chunk[i]) {
			if !utf8.FullRune(chunk[i:]) {
				return chunk[:i], append([]byte(nil), chunk[i:]...)
			}
			break
		}
	}
	return chunk, nil
}
