package mogenius

import (
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func TestExecuteCommandRequestAndResult(t *testing.T) {
	f := newFakePlatform(t,
		reply{status: 201, body: execJSON(3, "out\n", "err\n")},
		reply{status: 201, body: execJSON(0, "", "")},
	)
	sandbox := f.sandbox(t, nil)
	ctx := context.Background()

	response, err := sandbox.Process.ExecuteCommand(ctx, "ls /nope")
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 3 || response.Result != "out\nerr\n" || response.Artifacts.Stdout != "out\n" || response.Artifacts.Stderr != "err\n" {
		t.Fatalf("unexpected response %+v %+v", response, response.Artifacts)
	}
	_, err = sandbox.Process.ExecuteCommand(ctx, "pwd",
		options.WithCwd("/tmp"), options.WithCommandEnv(map[string]string{"A": "1"}), options.WithExecuteTimeout(1500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	calls := f.recorded()
	// commands are a pod feature: the pod route, in the sandbox's own container
	expectCall(t, calls[0], http.MethodPost, "/resource/exec/agent-sandbox/default-k27tp")
	if body := calls[0].json(t); !reflect.DeepEqual(body, map[string]any{"command": "ls /nope", "timeout": float64(10), "container": "sandbox"}) {
		t.Fatalf("default body %v", body)
	}
	want := map[string]any{"command": "pwd", "cwd": "/tmp", "env": map[string]any{"A": "1"}, "timeout": float64(2), "container": "sandbox"}
	if body := calls[1].json(t); !reflect.DeepEqual(body, want) {
		t.Fatalf("body %v, want %v", body, want)
	}
}

func TestExecuteCommandTimeoutIsAProcessExecutionTimeout(t *testing.T) {
	f := newFakePlatform(t, reply{status: 408, body: map[string]any{
		"statusCode": 408, "errorCode": "EXEC_TIMEOUT", "source": "operator", "message": "Command timed out after 10 s",
	}})

	_, err := f.sandbox(t, nil).Process.ExecuteCommand(context.Background(), "sleep 60")

	var process *sdkerrors.MogeniusProcessExecutionTimeoutError
	var timeout *sdkerrors.MogeniusTimeoutError
	if !errors.As(err, &process) || !errors.As(err, &timeout) || process.Source != sdkerrors.SourceOperator {
		t.Fatalf("got %v", err)
	}
}

func TestCodeRunRunsPythonThroughTheBootstrapAndExtractsCharts(t *testing.T) {
	chart := `{"type":"bar","title":"sales","png":"iVBOR","elements":[{"label":"a","points":[[1,2]]}]}`
	stdout := "hello\n" + chartMarker + chart + "\nbye\n"
	f := newFakePlatform(t, reply{status: 201, body: execJSON(0, stdout, "warn\n")})

	response, err := f.sandbox(t, nil).Process.CodeRun(context.Background(), `print("hello")`,
		options.WithCodeRunParams(types.CodeRunParams{Argv: []string{"a b"}, Env: map[string]string{"X": "1"}}),
		options.WithCodeRunTimeout(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	body := f.recorded()[0].json(t).(map[string]any)
	command := body["command"].(string)
	launcher := `python3 -c "$(printf %s '` + base64.StdEncoding.EncodeToString([]byte(pythonBootstrap)) + `' | base64 -d)"`
	if !strings.Contains(command, launcher+` "$__mo_f" 'a b'`) ||
		!strings.Contains(command, base64.StdEncoding.EncodeToString([]byte(`print("hello")`))) ||
		!strings.HasSuffix(command, "exit $__mo_rc") {
		t.Fatalf("command %q", command)
	}
	if !reflect.DeepEqual(body["env"], map[string]any{"X": "1"}) || body["timeout"] != float64(20) {
		t.Fatalf("body %v", body)
	}
	if response.Result != "hello\nbye\nwarn\n" || response.Artifacts.Stdout != "hello\nbye\n" || response.Artifacts.Stderr != "warn\n" {
		t.Fatalf("response %+v %+v", response, response.Artifacts)
	}
	charts := response.Artifacts.Charts
	if len(charts) != 1 || *charts[0].Type != "bar" || *charts[0].Title != "sales" || *charts[0].Png != "iVBOR" || len(charts[0].Elements) != 1 {
		t.Fatalf("charts %+v", charts)
	}
}

func TestCodeRunTakesTheLanguageOption(t *testing.T) {
	stdout := chartMarker + "{\"type\":\"line\"}\n"
	f := newFakePlatform(t, reply{status: 201, body: execJSON(0, stdout, "")})

	response, err := f.sandbox(t, nil).Process.CodeRun(context.Background(), "console.log(1)",
		options.WithCodeRunLanguage(types.CodeLanguageTypeScript))
	if err != nil {
		t.Fatal(err)
	}

	command := f.recorded()[0].json(t).(map[string]any)["command"].(string)
	if !strings.Contains(command, `tsx "$__mo_f"`) || !strings.Contains(command, "/tmp/mo_code_$$.ts") {
		t.Fatalf("command %q", command)
	}
	// only Python output is searched for charts
	if response.Result != stdout || response.Artifacts.Charts != nil {
		t.Fatalf("response %+v", response.Artifacts)
	}
}

func TestCodeRunHelpers(t *testing.T) {
	if quoted := shellQuote("it's"); quoted != `'it'\''s'` {
		t.Fatalf("shellQuote = %s", quoted)
	}
	if _, err := buildCodeRunCommand("x", "ruby", nil); err == nil {
		t.Fatal("an unknown language must fail")
	}

	// whole lines: a CRLF ending and a last line without one
	text, charts := extractCharts("a\n" + chartMarker + `{"type":"line"}` + "\r\nb\n" + chartMarker + `{"type":"pie"}`)
	if text != "a\nb\n" || len(charts) != 2 || *charts[0].Type != "line" || *charts[1].Type != "pie" {
		t.Fatalf("text %q, charts %+v", text, charts)
	}
	// malformed markers and markers inside a line stay
	stdout := "a\n" + chartMarker + "{not json}\nsaid " + chartMarker + `{"type":"line"}` + "\n"
	if text, charts := extractCharts(stdout); text != stdout || charts != nil {
		t.Fatalf("text %q, charts %+v", text, charts)
	}
}

// The TypeScript SDK starts Python snippets with the same program.
func TestPythonBootstrapMatchesTypeScript(t *testing.T) {
	source, err := os.ReadFile("../../../typescript/src/python-bootstrap.ts")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("the TypeScript SDK is not next to this module")
	}
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "String.raw`")
	end := strings.LastIndex(text, "`;")
	if start < 0 || end < start {
		t.Fatal("PYTHON_BOOTSTRAP not found in python-bootstrap.ts")
	}
	if text[start+len("String.raw`"):end] != pythonBootstrap {
		t.Fatal("pkg/mogenius/code_run.py and packages/typescript/src/python-bootstrap.ts differ; change both together")
	}
}

func TestSessionsRunOnThePodRoutes(t *testing.T) {
	session := map[string]any{
		"sessionId": "build", "container": "sandbox", "createdAt": "2026-10-01T12:00:00.000Z", "lastUsedAt": "2026-10-01T12:01:00.000Z",
		"commands": []any{map[string]any{"id": "c1", "command": "echo hi", "exitCode": 0}},
	}
	f := newFakePlatform(t,
		reply{status: 201, body: session},
		reply{status: 201, body: map[string]any{"cmdId": "c1", "exitCode": 0, "output": "hi\n"}},
		reply{status: 201, body: map[string]any{"cmdId": "c2", "exitCode": nil}},
		reply{body: map[string]any{"id": "c2", "command": "sleep 5", "exitCode": nil}},
		reply{body: map[string]any{"output": "partial"}},
		reply{status: 201},
		reply{body: session},
		reply{body: []any{session}},
		reply{status: 200},
	)
	process := f.sandbox(t, nil).Process
	ctx := context.Background()

	if err := process.CreateSession(ctx, "build"); err != nil {
		t.Fatal(err)
	}
	result, err := process.ExecuteSessionCommand(ctx, "build", "echo hi", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, map[string]any{"id": "c1", "exitCode": int32(0), "stdout": "hi\n", "output": "hi\n"}) {
		t.Fatalf("result %#v", result)
	}
	async, err := process.ExecuteSessionCommand(ctx, "build", "sleep 5", true, false)
	if err != nil || !reflect.DeepEqual(async, map[string]any{"id": "c2"}) {
		t.Fatalf("async %#v, %v", async, err)
	}
	command, err := process.GetSessionCommand(ctx, "build", "c2")
	if err != nil || !reflect.DeepEqual(command, map[string]any{"id": "c2", "command": "sleep 5"}) {
		t.Fatalf("command %#v, %v", command, err)
	}
	logs, err := process.GetSessionCommandLogs(ctx, "build", "c2")
	if err != nil || logs.Output != "partial" {
		t.Fatalf("logs %+v, %v", logs, err)
	}
	if err := process.SendSessionCommandInput(ctx, "build", "c2", "y\n"); err != nil {
		t.Fatal(err)
	}
	got, err := process.GetSession(ctx, "build")
	if err != nil {
		t.Fatal(err)
	}
	commands := got["commands"].([]map[string]any)
	if got["sessionId"] != "build" || got["container"] != "sandbox" || len(commands) != 1 || commands[0]["exitCode"] != int32(0) {
		t.Fatalf("session %#v", got)
	}
	if sessions, err := process.ListSessions(ctx); err != nil || len(sessions) != 1 || sessions[0]["sessionId"] != "build" {
		t.Fatalf("sessions %#v, %v", sessions, err)
	}
	if err := process.DeleteSession(ctx, "build"); err != nil {
		t.Fatal(err)
	}

	calls := f.recorded()
	base := "/resource/session/agent-sandbox/default-k27tp"
	for i, want := range []struct{ method, path string }{
		{http.MethodPost, base},
		{http.MethodPost, base + "/build/exec"},
		{http.MethodPost, base + "/build/exec"},
		{http.MethodGet, base + "/build/command/c2"},
		{http.MethodGet, base + "/build/command/c2/logs"},
		{http.MethodPost, base + "/build/command/c2/input"},
		{http.MethodGet, base + "/build"},
		{http.MethodGet, base},
		{http.MethodDelete, base + "/build"},
	} {
		expectCall(t, calls[i], want.method, want.path)
	}
	for i, want := range map[int]map[string]any{
		0: {"sessionId": "build", "container": "sandbox"},
		1: {"command": "echo hi"},
		2: {"command": "sleep 5", "runAsync": true},
		5: {"data": "y\n"},
	} {
		if body := calls[i].json(t); !reflect.DeepEqual(body, want) {
			t.Errorf("call %d body %v, want %v", i, body, want)
		}
	}
}

func TestSessionsNeedAPod(t *testing.T) {
	stillCreating := sandboxJSON(map[string]any{"podName": nil, "state": "creating"})
	f := newFakePlatform(t, reply{body: stillCreating})
	process := f.sandbox(t, stillCreating).Process

	var conflict *sdkerrors.MogeniusConflictError
	if err := process.CreateSession(context.Background(), "build"); !errors.As(err, &conflict) {
		t.Fatalf("got %v", err)
	}
	// the sandbox was read again, no session route was asked
	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("got %d calls", len(calls))
	}
	expectCall(t, calls[0], http.MethodGet, "/sandbox/agent-sandbox/default-abc12")
}

func TestASandboxWithoutAPodIsReadAgain(t *testing.T) {
	f := newFakePlatform(t,
		reply{body: sandboxJSON(nil)},
		reply{status: 201, body: execJSON(0, "", "")},
	)
	process := f.sandbox(t, map[string]any{"podName": nil, "state": "creating"}).Process

	if _, err := process.ExecuteCommand(context.Background(), "true"); err != nil {
		t.Fatal(err)
	}
	calls := f.recorded()
	expectCall(t, calls[0], http.MethodGet, "/sandbox/agent-sandbox/default-abc12")
	expectCall(t, calls[1], http.MethodPost, "/resource/exec/agent-sandbox/default-k27tp")
}

func TestTheContainerCanBeChosen(t *testing.T) {
	f := newFakePlatform(t,
		reply{status: 201, body: execJSON(0, "", "")},
		reply{status: 201, body: execJSON(0, "", "")},
		reply{status: 201},
	)
	process := f.sandbox(t, nil).Process
	ctx := context.Background()

	if _, err := process.ExecuteCommand(ctx, "ls", options.WithContainer("sidecar")); err != nil {
		t.Fatal(err)
	}
	if _, err := process.CodeRun(ctx, "print(1)", options.WithCodeRunContainer("sidecar")); err != nil {
		t.Fatal(err)
	}
	if err := process.CreateSession(ctx, "build", options.WithSessionContainer("sidecar")); err != nil {
		t.Fatal(err)
	}

	for i, call := range f.recorded() {
		if container := call.json(t).(map[string]any)["container"]; container != "sidecar" {
			t.Errorf("call %d (%s) sent container %v", i, call.path, container)
		}
	}
}
