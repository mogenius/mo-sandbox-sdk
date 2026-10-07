package mogenius

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
)

func TestSandboxLifecycleRoutes(t *testing.T) {
	fastPolling(t)
	f := newFakePlatform(t,
		reply{status: 201, body: sandboxJSON(map[string]any{"state": "stopping"})},
		reply{body: sandboxJSON(map[string]any{"state": "stopped", "podName": nil})},
		reply{status: 201, body: sandboxJSON(map[string]any{"state": "starting"})},
		reply{body: sandboxJSON(nil)},
		reply{body: sandboxJSON(map[string]any{"labels": map[string]any{"team": "a"}})},
		reply{body: sandboxJSON(map[string]any{"expiresAt": "2026-10-01T12:30:00.000Z"})},
		reply{body: sandboxJSON(nil)},
		reply{body: sandboxJSON(map[string]any{"state": "destroying"})},
	)
	sandbox := f.sandbox(t, nil)
	ctx := context.Background()
	thirty := 30

	if err := sandbox.Stop(ctx); err != nil || sandbox.State != SandboxStateStopped || sandbox.PodName != nil {
		t.Fatalf("stop: %v, %+v", err, sandbox)
	}
	if err := sandbox.Start(ctx); err != nil || sandbox.State != SandboxStateStarted {
		t.Fatalf("start: %v, %s", err, sandbox.State)
	}
	if err := sandbox.SetLabels(ctx, map[string]string{"team": "a"}); err != nil || sandbox.Labels["team"] != "a" {
		t.Fatalf("labels: %v, %v", err, sandbox.Labels)
	}
	if err := sandbox.SetAutoDeleteInterval(ctx, &thirty); err != nil || sandbox.ExpiresAt == nil {
		t.Fatalf("ttl: %v, %v", err, sandbox.ExpiresAt)
	}
	if err := sandbox.SetAutoDeleteInterval(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Delete(ctx); err != nil || sandbox.State != SandboxStateDestroying {
		t.Fatalf("delete: %v, %s", err, sandbox.State)
	}

	calls := f.recorded()
	path := "/sandbox/agent-sandbox/default-abc12"
	for i, want := range []struct{ method, path string }{
		{http.MethodPost, path + "/stop"},
		{http.MethodGet, path},
		{http.MethodPost, path + "/start"},
		{http.MethodGet, path},
		{http.MethodPatch, path},
		{http.MethodPatch, path},
		{http.MethodPatch, path},
		{http.MethodDelete, path},
	} {
		expectCall(t, calls[i], want.method, want.path)
	}
	if labels := calls[4].json(t).(map[string]any)["labels"].(map[string]any); labels["team"] != "a" {
		t.Fatalf("labels body %v", labels)
	}
	if ttl := calls[5].json(t).(map[string]any)["ttlMinutes"]; ttl != float64(30) {
		t.Fatalf("ttl body %v", ttl)
	}
	if ttl := calls[6].json(t).(map[string]any)["ttlMinutes"]; ttl != float64(0) {
		t.Fatalf("nil interval should remove the deadline, sent %v", ttl)
	}
}

func TestWaitForStartFailsOnTheErrorState(t *testing.T) {
	fastPolling(t)
	f := newFakePlatform(t, reply{body: sandboxJSON(map[string]any{"state": "error", "stateReason": "ImagePullBackOff"})})
	sandbox := f.sandbox(t, map[string]any{"state": "starting"})

	err := sandbox.WaitForStart(context.Background(), time.Second)

	var conflict *sdkerrors.MogeniusConflictError
	if !errors.As(err, &conflict) || conflict.ErrorCode != "SANDBOX_ERROR" || !strings.Contains(conflict.Message, "ImagePullBackOff") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitForStartTimesOut(t *testing.T) {
	fastPolling(t)
	f := newFakePlatform(t, reply{body: sandboxJSON(map[string]any{"state": "starting"})})
	sandbox := f.sandbox(t, map[string]any{"state": "starting"})

	err := sandbox.WaitForStart(context.Background(), 20*time.Millisecond)

	var timeout *sdkerrors.MogeniusTimeoutError
	if !errors.As(err, &timeout) || !strings.Contains(timeout.Message, `still "starting"`) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitForStartFollowsTheContext(t *testing.T) {
	f := newFakePlatform(t, reply{body: sandboxJSON(map[string]any{"state": "starting"})})
	sandbox := f.sandbox(t, map[string]any{"state": "starting"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sandbox.WaitForStart(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestUnsupportedFeaturesSayWhatToDoInstead(t *testing.T) {
	f := newFakePlatform(t)
	sandbox := f.sandbox(t, nil)
	ctx := context.Background()
	_, forkErr := sandbox.ExperimentalFork(ctx, nil)

	for name, err := range map[string]error{
		"Archive":                    sandbox.Archive(ctx),
		"Pause":                      sandbox.Pause(ctx),
		"SetAutoArchiveInterval":     sandbox.SetAutoArchiveInterval(ctx, nil),
		"ExperimentalCreateSnapshot": sandbox.ExperimentalCreateSnapshot(ctx, "x"),
		"ExperimentalFork":           forkErr,
	} {
		var unsupported *sdkerrors.MogeniusUnsupportedError
		if !errors.As(err, &unsupported) || unsupported.ErrorCode != "UNSUPPORTED" || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if len(f.recorded()) != 0 {
		t.Fatal("unsupported calls must not reach the platform")
	}
}

func TestHomeAndWorkingDirectory(t *testing.T) {
	f := newFakePlatform(t,
		reply{status: 201, body: execJSON(0, "/home/sandbox", "")},
		reply{status: 201, body: execJSON(0, "/workspace\n", "")},
		reply{status: 201, body: execJSON(1, "", "pwd: cannot access")},
	)
	sandbox := f.sandbox(t, nil)
	ctx := context.Background()

	home, err := sandbox.GetUserHomeDir(ctx)
	if err != nil || home != "/home/sandbox" {
		t.Fatalf("home %q, %v", home, err)
	}
	if command := f.recorded()[0].json(t).(map[string]any)["command"]; command != `printf %s "$HOME"` {
		t.Fatalf("command %v", command)
	}
	if dir, err := sandbox.GetWorkingDir(ctx); err != nil || dir != "/workspace" {
		t.Fatalf("workdir %q, %v", dir, err)
	}
	var conflict *sdkerrors.MogeniusConflictError
	if _, err := sandbox.GetWorkingDir(ctx); !errors.As(err, &conflict) || !strings.Contains(conflict.Message, "exit code 1") {
		t.Fatalf("got %v", err)
	}
}
