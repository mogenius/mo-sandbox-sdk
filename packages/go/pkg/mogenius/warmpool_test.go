package mogenius

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func warmPoolJSON(name string, replicas, ready int) map[string]any {
	return warmPoolInJSON("agent-sandbox", name, replicas, ready)
}

func warmPoolInJSON(namespace, name string, replicas, ready int) map[string]any {
	return map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxWarmPool",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"replicas": replicas, "sandboxTemplateRef": map[string]any{"name": name + "-template"}},
		"status":     map[string]any{"readyReplicas": ready},
	}
}

func TestWarmPoolsFromItems(t *testing.T) {
	f := newFakePlatform(t, reply{body: map[string]any{
		"status": "success",
		"data":   map[string]any{"kind": "List", "items": []any{warmPoolJSON("python", 2, 1), warmPoolJSON("default", 3, 3)}},
	}})
	pools, err := f.client(t).WarmPools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []types.WarmPool{
		{Name: "default", Namespace: "agent-sandbox", Template: "default-template", Ready: 3, Size: 3},
		{Name: "python", Namespace: "agent-sandbox", Template: "python-template", Ready: 1, Size: 2},
	}
	if len(pools) != len(want) || pools[0] != want[0] || pools[1] != want[1] {
		t.Fatalf("pools = %+v", pools)
	}

	call := f.recorded()[0]
	if call.method != http.MethodGet || call.path != "/resource/workload/list" {
		t.Fatalf("called %s %s", call.method, call.path)
	}
	for key, value := range map[string]string{
		"kind": "SandboxWarmPool", "plural": "sandboxwarmpools", "apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"namespaced": "true", "namespace": "agent-sandbox", "withData": "false",
	} {
		if got := call.query.Get(key); got != value {
			t.Errorf("query %s = %q, want %q", key, got, value)
		}
	}
	if call.header.Get("cluster-id") != "cluster-1" || call.header.Get("organization-id") != "org-1" {
		t.Errorf("headers = %v", call.header)
	}
}

func TestWarmPoolsOfAllNamespaces(t *testing.T) {
	f := newFakePlatform(t, reply{body: map[string]any{
		"status": "success",
		"data": map[string]any{"items": []any{
			warmPoolInJSON("team-b", "default", 1, 1),
			warmPoolInJSON("agent-sandbox", "python", 2, 2),
			warmPoolInJSON("team-a", "gpu", 1, 0),
			warmPoolInJSON("agent-sandbox", "default", 3, 3),
		}},
	}})
	pools, err := f.client(t).WarmPools(context.Background(), options.WithAllNamespaces())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pools {
		got = append(got, p.Namespace+"/"+p.Name)
	}
	if strings.Join(got, ",") != "agent-sandbox/default,agent-sandbox/python,team-a/gpu,team-b/default" {
		t.Fatalf("order = %v", got)
	}
	call := f.recorded()[0]
	if _, ok := call.query["namespace"]; ok {
		t.Errorf("namespace sent for all namespaces: %q", call.query.Get("namespace"))
	}
	if call.query.Get("kind") != "SandboxWarmPool" || call.query.Get("withData") != "false" {
		t.Errorf("query = %v", call.query)
	}
}

func TestWarmPoolsFromBareListWithMissingFields(t *testing.T) {
	f := newFakePlatform(t, reply{body: map[string]any{
		"status": "success",
		"data":   []any{map[string]any{"metadata": map[string]any{"name": "fresh", "namespace": "agent-sandbox"}}},
	}})
	pools, err := f.client(t).WarmPools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || pools[0] != (types.WarmPool{Name: "fresh", Namespace: "agent-sandbox"}) {
		t.Fatalf("pools = %+v", pools)
	}
}

func TestWarmPoolsEmpty(t *testing.T) {
	f := newFakePlatform(t, reply{body: map[string]any{"status": "success", "data": nil}})
	pools, err := f.client(t).WarmPools(context.Background())
	if err != nil || pools == nil || len(pools) != 0 {
		t.Fatalf("pools = %#v, %v", pools, err)
	}
}

func TestWarmPoolsWithoutTheResourceType(t *testing.T) {
	for name, answer := range map[string]reply{
		"operator 404":  {body: map[string]any{"status": "error", "statusCode": 404, "message": "sandboxwarmpools not found"}},
		"operator text": {body: map[string]any{"status": "error", "message": "the server could not find the requested resource"}},
		"api 400":       {status: 400, body: map[string]any{"message": `no matches for kind "SandboxWarmPool" in version "extensions.agents.x-k8s.io/v1beta1"`}},
		"api 404":       {status: 404, body: map[string]any{"message": "not found"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakePlatform(t, answer)
			pools, err := f.client(t).WarmPools(context.Background())
			if err != nil || pools == nil || len(pools) != 0 {
				t.Fatalf("pools = %#v, %v", pools, err)
			}
		})
	}
}

func TestWarmPoolsFailures(t *testing.T) {
	f := newFakePlatform(t, reply{status: 403, body: map[string]any{"message": "no access to this cluster"}})
	if _, err := f.client(t).WarmPools(context.Background()); !errors.As(err, new(*sdkerrors.MogeniusForbiddenError)) {
		t.Fatalf("403 = %v", err)
	}

	f = newFakePlatform(t, reply{body: map[string]any{"status": "error", "statusCode": 500, "message": "etcd timeout"}})
	_, err := f.client(t).WarmPools(context.Background())
	var base *sdkerrors.MogeniusError
	if !errors.As(err, &base) || base.Source != sdkerrors.SourceOperator || base.StatusCode != 500 || base.Message != "listing warm pools: etcd timeout" {
		t.Fatalf("operator error = %v (%+v)", err, base)
	}
}
