package mogenius

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// warmPoolListQuery names the SandboxWarmPool type the way the platform's
// generic resource routes take it (DefaultResourceDescriptor.SandboxWarmPool
// of the platform's client SDK); the data of the objects is left out. An empty
// namespace is left out too, and the platform lists every namespace.
func warmPoolListQuery(namespace string) url.Values {
	return query(
		"kind", "SandboxWarmPool",
		"plural", "sandboxwarmpools",
		"apiVersion", "extensions.agents.x-k8s.io/v1beta1",
		"namespaced", "true",
		"namespace", namespace,
		"withData", "false",
	)
}

// workloadListResponse is the operator's answer as the platform relays it:
// an UnstructuredList in data (data.items), or a bare list.
type workloadListResponse struct {
	Status     string          `json:"status"`
	Message    string          `json:"message"`
	StatusCode int             `json:"statusCode"`
	Data       json.RawMessage `json:"data"`
}

// warmPoolObject is the part of a SandboxWarmPool the SDK reads.
type warmPoolObject struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Spec struct {
		Replicas           int `json:"replicas"`
		SandboxTemplateRef struct {
			Name string `json:"name"`
		} `json:"sandboxTemplateRef"`
	} `json:"spec"`
	Status struct {
		ReadyReplicas int `json:"readyReplicas"`
	} `json:"status"`
}

// WarmPools lists the warm pools of the client's namespace (mogenius), or of
// every namespace with options.WithAllNamespaces, sorted by namespace and name.
// A cluster without the agent-sandbox resource types has none: that is an
// empty list, not an error.
func (c *Client) WarmPools(ctx context.Context, opts ...func(*options.ListWarmPools)) ([]types.WarmPool, error) {
	listOpts := &options.ListWarmPools{}
	for _, opt := range opts {
		opt(listOpts)
	}
	namespace := c.namespace
	if listOpts.AllNamespaces {
		namespace = ""
	}
	var answer workloadListResponse
	if err := c.api.get(ctx, "/resource/workload/list", warmPoolListQuery(namespace), &answer); err != nil {
		if missingResourceType(err) {
			return []types.WarmPool{}, nil
		}
		return nil, err
	}
	if answer.Status == "error" {
		if answer.StatusCode == 404 || missingTypeMessage(answer.Message) {
			return []types.WarmPool{}, nil
		}
		base := sdkerrors.NewMogeniusError("listing warm pools: "+answer.Message, answer.StatusCode, nil)
		base.Source = sdkerrors.SourceOperator
		return nil, base
	}
	objects, err := decodeWorkloadItems(answer.Data)
	if err != nil {
		base := sdkerrors.NewMogeniusError("listing warm pools: unexpected answer: "+err.Error(), 0, nil)
		base.Source = sdkerrors.SourceAPI
		return nil, base
	}
	pools := make([]types.WarmPool, 0, len(objects))
	for _, o := range objects {
		pools = append(pools, types.WarmPool{
			Name:      o.Metadata.Name,
			Namespace: o.Metadata.Namespace,
			Template:  o.Spec.SandboxTemplateRef.Name,
			Ready:     o.Status.ReadyReplicas,
			Size:      o.Spec.Replicas,
		})
	}
	sort.Slice(pools, func(i, j int) bool {
		if pools[i].Namespace != pools[j].Namespace {
			return pools[i].Namespace < pools[j].Namespace
		}
		return pools[i].Name < pools[j].Name
	})
	return pools, nil
}

// decodeWorkloadItems reads data.items, a bare list, or nothing.
func decodeWorkloadItems(data json.RawMessage) ([]warmPoolObject, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []warmPoolObject
		err := json.Unmarshal(data, &items)
		return items, err
	}
	var list struct {
		Items []warmPoolObject `json:"items"`
	}
	err := json.Unmarshal(data, &list)
	return list.Items, err
}

// missingResourceType tells a failed list whose resource type the cluster
// does not know (agent-sandbox not installed) from a real failure.
func missingResourceType(err error) bool {
	var notFound *sdkerrors.MogeniusNotFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var base *sdkerrors.MogeniusError
	return errors.As(err, &base) && base.StatusCode == 400 && missingTypeMessage(base.Message)
}

// missingTypeMessage matches Kubernetes' answers for an unknown resource type.
func missingTypeMessage(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "could not find the requested resource") ||
		strings.Contains(m, "no matches for kind") ||
		strings.Contains(m, "doesn't have a resource type")
}
