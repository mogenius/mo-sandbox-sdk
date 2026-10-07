package mogenius

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func TestConfigNeedsAKey(t *testing.T) {
	newFakePlatform(t)
	_, err := NewClient()
	var base *sdkerrors.MogeniusError
	if !errors.As(err, &base) || !strings.Contains(base.Message, "MOGENIUS_API_KEY") {
		t.Fatalf("got %v", err)
	}
}

func TestConfigFallsBackToTheEnvironment(t *testing.T) {
	newFakePlatform(t)
	t.Setenv("MOGENIUS_API_KEY", " mo_pat:env ")
	t.Setenv("MOGENIUS_API_URL", "https://api.example.com/")
	t.Setenv("MOGENIUS_SANDBOX_NAMESPACE", "sandboxes")

	config, err := resolveConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if config.apiKey != "mo_pat:env" || config.apiURL != "https://api.example.com" || config.streamURL != DefaultStreamURL ||
		config.namespace != "sandboxes" || config.httpClient != http.DefaultClient {
		t.Fatalf("unexpected config %+v", config)
	}

	// explicit config wins; Target is the namespace
	config, err = resolveConfig(&types.MogeniusConfig{APIKey: "mo_pat:own", Target: "from-target"})
	if err != nil {
		t.Fatal(err)
	}
	if config.apiKey != "mo_pat:own" || config.namespace != "from-target" {
		t.Fatalf("unexpected config %+v", config)
	}
}

func TestCreateFromASnapshot(t *testing.T) {
	f := newFakePlatform(t, reply{status: 201, body: sandboxJSON(map[string]any{"labels": map[string]any{"project": "demo"}})})
	interval := 30

	sandbox, err := f.client(t).Create(context.Background(), types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{
			Labels:             map[string]string{"project": "demo"},
			EnvVars:            map[string]string{"A": "1"},
			AutoDeleteInterval: &interval,
			Ephemeral:          true,
		},
		Snapshot: "gpu",
	})
	if err != nil {
		t.Fatal(err)
	}

	calls := f.recorded()
	if len(calls) != 1 {
		t.Fatalf("got %d calls", len(calls))
	}
	expectCall(t, calls[0], http.MethodPost, "/sandbox/agent-sandbox")
	for header, want := range map[string]string{
		"Authorization":   "Bearer mo_pat:user:secret",
		"organization-id": "org-1",
		"cluster-id":      "cluster-1",
		"Content-Type":    "application/json",
		"User-Agent":      "mogenius-sandbox-go",
	} {
		if got := calls[0].header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	want := map[string]any{
		"profile":            "gpu",
		"labels":             map[string]any{"project": "demo"},
		"env":                map[string]any{"A": "1"},
		"ttlMinutes":         float64(30),
		"ephemeral":          true,
		"waitForStart":       true,
		"waitTimeoutSeconds": float64(60),
	}
	if body := calls[0].json(t); !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
	if sandbox.ID != "default-abc12" || sandbox.State != SandboxStateStarted || *sandbox.PodName != "default-k27tp" ||
		sandbox.Labels["project"] != "demo" || *sandbox.Snapshot != "default" || sandbox.Target != "agent-sandbox" ||
		sandbox.ContainerName != "sandbox" || sandbox.Kind != "SandboxClaim" {
		t.Fatalf("unexpected sandbox %+v", sandbox)
	}
}

func TestCreateFromAnImageWithoutWaiting(t *testing.T) {
	f := newFakePlatform(t, reply{status: 201, body: sandboxJSON(map[string]any{"kind": "Sandbox", "state": "creating", "podName": nil})})

	sandbox, err := f.client(t).Create(context.Background(), &types.ImageParams{
		SandboxBaseParams: types.SandboxBaseParams{Name: "mine"},
		Image:             "python:3.12-slim",
		Snapshot:          "default",
	}, options.WithWaitForStart(false))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"name": "mine", "profile": "default", "image": "python:3.12-slim", "waitForStart": false}
	if body := f.recorded()[0].json(t); !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %v, want %v", body, want)
	}
	if sandbox.State != SandboxStateCreating || sandbox.PodName != nil || len(f.recorded()) != 1 {
		t.Fatalf("unexpected sandbox %+v", sandbox)
	}
}

func TestCreateFinishesTheWaitThePlatformStarted(t *testing.T) {
	fastPolling(t)
	f := newFakePlatform(t,
		reply{status: 201, body: sandboxJSON(map[string]any{"state": "starting"})},
		reply{body: sandboxJSON(map[string]any{"state": "starting"})},
		reply{body: sandboxJSON(nil)},
	)

	sandbox, err := f.client(t).Create(context.Background(), nil, options.WithTimeout(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	calls := f.recorded()
	if body := calls[0].json(t).(map[string]any); body["waitTimeoutSeconds"] != float64(90) {
		t.Fatalf("body = %v", body)
	}
	if len(calls) != 3 {
		t.Fatalf("got %d calls", len(calls))
	}
	expectCall(t, calls[1], http.MethodGet, "/sandbox/agent-sandbox/default-abc12")
	if sandbox.State != SandboxStateStarted {
		t.Fatalf("state %s", sandbox.State)
	}
}

func TestCreateRejectsWhatItCannotSend(t *testing.T) {
	f := newFakePlatform(t)
	client := f.client(t)

	var validation *sdkerrors.MogeniusValidationError
	if _, err := client.Create(context.Background(), "python"); !errors.As(err, &validation) {
		t.Fatalf("params: got %v", err)
	}
	params := types.SnapshotParams{SandboxBaseParams: types.SandboxBaseParams{Language: "ruby"}}
	if _, err := client.Create(context.Background(), params); !errors.As(err, &validation) {
		t.Fatalf("language: got %v", err)
	}
	if len(f.recorded()) != 0 {
		t.Fatal("nothing should have been sent")
	}
}

func TestCreateClosesTheLogChannel(t *testing.T) {
	f := newFakePlatform(t, reply{status: 201, body: sandboxJSON(nil)})
	logs := make(chan string, 1)

	if _, err := f.client(t).Create(context.Background(), nil, options.WithLogChannel(logs)); err != nil {
		t.Fatal(err)
	}
	if _, open := <-logs; open {
		t.Fatal("the log channel is still open")
	}
}

func TestGetMapsPlatformErrors(t *testing.T) {
	f := newFakePlatform(t, reply{status: 404, body: map[string]any{
		"statusCode": 404, "errorCode": "SANDBOX_NOT_FOUND", "source": "api", "message": "Sandbox a/b not found",
	}})

	_, err := f.client(t).Get(context.Background(), "a/b")

	var notFound *sdkerrors.MogeniusNotFoundError
	if !errors.As(err, &notFound) || notFound.ErrorCode != "SANDBOX_NOT_FOUND" || notFound.StatusCode != 404 ||
		notFound.Message != "Sandbox a/b not found" || notFound.Source != sdkerrors.SourceAPI {
		t.Fatalf("got %v", err)
	}
	expectCall(t, f.recorded()[0], http.MethodGet, "/sandbox/agent-sandbox/a%2Fb")
}

func TestListPagesThroughCursors(t *testing.T) {
	f := newFakePlatform(t,
		reply{body: map[string]any{"items": []any{sandboxJSON(map[string]any{"id": "a"})}, "nextCursor": "c2", "totalCount": 2}},
		reply{body: map[string]any{"items": []any{sandboxJSON(map[string]any{"id": "b"})}, "nextCursor": nil, "totalCount": 2}},
	)
	limit := 1

	it := f.client(t).List(context.Background(), &ListSandboxesQuery{
		Limit:  &limit,
		Labels: map[string]string{"b": "2", "a": "1"},
		States: []SandboxState{SandboxStateStarted},
	})
	var ids []string
	for it.Next() {
		ids = append(ids, it.Value().ID)
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}

	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ids %v", ids)
	}
	calls := f.recorded()
	first := calls[0].query
	if first.Get("labels") != "a=1,b=2" || first.Get("state") != "started" || first.Get("limit") != "1" || first.Has("cursor") {
		t.Fatalf("first page query %v", first)
	}
	if calls[1].query.Get("cursor") != "c2" {
		t.Fatalf("second page query %v", calls[1].query)
	}
}

func TestListSeqFiltersSeveralStatesHere(t *testing.T) {
	f := newFakePlatform(t, reply{body: map[string]any{"items": []any{
		sandboxJSON(map[string]any{"id": "a"}),
		sandboxJSON(map[string]any{"id": "b", "state": "stopped"}),
		sandboxJSON(map[string]any{"id": "c", "state": "error"}),
	}, "nextCursor": nil, "totalCount": 3}})

	var ids []string
	for sandbox, err := range f.client(t).ListSeq(context.Background(), &ListSandboxesQuery{
		States: []SandboxState{SandboxStateStarted, SandboxStateStopped},
	}) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sandbox.ID)
	}

	if !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("ids %v", ids)
	}
	if f.recorded()[0].query.Has("state") {
		t.Fatal("several states are not sent")
	}
}

func TestRequestsWithoutAnAnswer(t *testing.T) {
	f := newFakePlatform(t)
	client := f.client(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// a done context comes back as itself
	if _, err := client.Get(ctx, "default-abc12"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: got %v", err)
	}
	f.server.Close()
	var base *sdkerrors.MogeniusError
	if _, err := client.Get(context.Background(), "default-abc12"); !errors.As(err, &base) || !strings.Contains(base.Message, "failed") {
		t.Fatalf("unreachable: got %v", err)
	}
}

func TestListStopsAtAnError(t *testing.T) {
	f := newFakePlatform(t, reply{status: 403, body: map[string]any{"statusCode": 403, "message": "no cluster role"}})

	it := f.client(t).List(context.Background(), nil)
	var forbidden *sdkerrors.MogeniusForbiddenError
	if it.Next() || !errors.As(it.Err(), &forbidden) {
		t.Fatalf("got %v", it.Err())
	}
}
