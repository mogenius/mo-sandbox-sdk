// Package mogenius is the Go SDK for mogenius sandboxes: isolated, short-lived
// environments for AI agents and code execution, running as pods in your own
// Kubernetes cluster.
//
//	client, err := mogenius.NewClient()                // MOGENIUS_API_KEY etc. from the environment
//	sandbox, err := client.Create(ctx, nil)            // claimed from the default warm pool, started
//	result, err := sandbox.Process.ExecuteCommand(ctx, "echo hello")
//	err = sandbox.Delete(ctx)
//
// Sandboxes run under your RBAC, through the mogenius operator; nothing
// leaves the cluster but the API calls.
package mogenius

import (
	"context"
	"fmt"
	"iter"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

const (
	// defaultCreateTimeout is how long Create waits for the start by default.
	defaultCreateTimeout = 60 * time.Second
	// platformWaitLimit is the longest the platform waits for a start inside the create call.
	platformWaitLimit = 300 * time.Second
)

// Client creates, finds and lists sandboxes in one namespace of one cluster.
type Client struct {
	api       *apiClient
	namespace string
}

// NewClient builds a client from the environment: MOGENIUS_API_KEY and,
// where the key does not decide them, MOGENIUS_ORGANIZATION_ID and
// MOGENIUS_CLUSTER_ID.
func NewClient() (*Client, error) {
	return NewClientWithConfig(nil)
}

// NewClientWithConfig builds a client; empty fields fall back to the environment.
func NewClientWithConfig(config *types.MogeniusConfig) (*Client, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{api: &apiClient{cfg: resolved}, namespace: resolved.namespace}, nil
}

// Close releases the client. It holds nothing between calls, so there is
// nothing to release; it exists for programs that defer it.
func (c *Client) Close(ctx context.Context) error {
	return nil
}

// Create creates a sandbox and, by default, waits until it is started.
// params is nil (the default profile's warm pool), types.SnapshotParams or
// types.ImageParams, as a value or a pointer. From a snapshot the sandbox is
// claimed from the profile's warm pool and ready in seconds; from an image a
// pod is started from the profile's template with that image.
func (c *Client) Create(ctx context.Context, params any, opts ...func(*options.CreateSandbox)) (*Sandbox, error) {
	createOpts := &options.CreateSandbox{WaitForStart: true}
	for _, opt := range opts {
		opt(createOpts)
	}
	if createOpts.LogChannel != nil {
		defer close(createOpts.LogChannel)
	}
	timeout := defaultCreateTimeout
	if createOpts.Timeout != nil {
		timeout = *createOpts.Timeout
	}

	var base types.SandboxBaseParams
	var snapshot, image string
	switch p := params.(type) {
	case nil:
	case types.SnapshotParams:
		base, snapshot = p.SandboxBaseParams, p.Snapshot
	case *types.SnapshotParams:
		if p != nil {
			base, snapshot = p.SandboxBaseParams, p.Snapshot
		}
	case types.ImageParams:
		base, snapshot, image = p.SandboxBaseParams, p.Snapshot, p.Image
	case *types.ImageParams:
		if p != nil {
			base, snapshot, image = p.SandboxBaseParams, p.Snapshot, p.Image
		}
	default:
		return nil, sdkerrors.NewMogeniusValidationError(
			fmt.Sprintf("Create takes types.SnapshotParams or types.ImageParams, not %T.", params), nil)
	}
	language := base.Language
	if language == "" {
		language = types.CodeLanguagePython
	}
	if _, ok := interpreters[language]; !ok {
		return nil, sdkerrors.NewMogeniusValidationError(fmt.Sprintf("Unknown language %q: use python, typescript or javascript.", language), nil)
	}

	body := map[string]any{}
	if base.Name != "" {
		body["name"] = base.Name
	}
	if snapshot != "" {
		body["profile"] = snapshot
	}
	if image != "" {
		body["image"] = image
	}
	if len(base.Labels) > 0 {
		body["labels"] = base.Labels
	}
	if len(base.EnvVars) > 0 {
		body["env"] = base.EnvVars
	}
	if base.AutoDeleteInterval != nil && *base.AutoDeleteInterval > 0 {
		body["ttlMinutes"] = *base.AutoDeleteInterval
	}
	if base.Ephemeral {
		body["ephemeral"] = true
	}
	wait := createOpts.WaitForStart && timeout > 0
	body["waitForStart"] = wait
	if wait {
		body["waitTimeoutSeconds"] = ceilSeconds(min(timeout, platformWaitLimit))
	}

	var dto sandboxDTO
	if err := c.api.post(ctx, "/sandbox/"+segment(c.namespace), nil, body, &dto); err != nil {
		return nil, err
	}
	sandbox := newSandbox(c.api, dto, language)
	if wait && sandbox.State != SandboxStateStarted {
		// the platform waited as long as it may; finish the wait here with what is left
		if err := sandbox.WaitForStart(ctx, max(time.Second, timeout-platformWaitLimit)); err != nil {
			return sandbox, err
		}
	}
	return sandbox, nil
}

// Get returns the sandbox with that id (claim or sandbox name).
func (c *Client) Get(ctx context.Context, sandboxIDOrName string) (*Sandbox, error) {
	var dto sandboxDTO
	if err := c.api.get(ctx, sandboxPath(c.namespace, sandboxIDOrName), nil, &dto); err != nil {
		return nil, err
	}
	return newSandbox(c.api, dto, types.CodeLanguagePython), nil
}

// ListSandboxesQuery filters List.
type ListSandboxesQuery struct {
	// Limit is the page size of each request to the platform.
	Limit *int
	// Labels a sandbox must all carry.
	Labels map[string]string
	// States a sandbox must be in, any of them.
	States []SandboxState
}

// SandboxIterator walks the sandboxes of a List, page by page:
//
//	it := client.List(ctx, nil)
//	for it.Next() {
//		sandbox := it.Value()
//	}
//	if err := it.Err(); err != nil { … }
type SandboxIterator struct {
	client    *Client
	ctx       context.Context
	query     *ListSandboxesQuery
	cursor    string
	page      []*Sandbox
	index     int
	started   bool
	exhausted bool
	current   *Sandbox
	err       error
}

// List returns an iterator over the sandboxes in the namespace that match the query (nil for all).
func (c *Client) List(ctx context.Context, query *ListSandboxesQuery) *SandboxIterator {
	if query == nil {
		query = &ListSandboxesQuery{}
	}
	return &SandboxIterator{client: c, ctx: ctx, query: query}
}

// ListSeq is List as a range-over-func sequence; an error ends it.
func (c *Client) ListSeq(ctx context.Context, query *ListSandboxesQuery) iter.Seq2[*Sandbox, error] {
	return func(yield func(*Sandbox, error) bool) {
		it := c.List(ctx, query)
		for it.Next() {
			if !yield(it.Value(), nil) {
				return
			}
		}
		if err := it.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// Next advances to the next sandbox, fetching the next page when needed.
func (it *SandboxIterator) Next() bool {
	if it.err != nil {
		return false
	}
	for {
		if it.index < len(it.page) {
			it.current = it.page[it.index]
			it.index++
			return true
		}
		if it.started && it.exhausted {
			return false
		}
		items, next, err := it.client.fetchPage(it.ctx, it.query, it.cursor)
		it.started = true
		if err != nil {
			it.err = err
			return false
		}
		it.page, it.index, it.cursor = items, 0, next
		it.exhausted = next == ""
	}
}

// Value is the sandbox Next advanced to.
func (it *SandboxIterator) Value() *Sandbox {
	return it.current
}

// Err is the error that ended the iteration, if any.
func (it *SandboxIterator) Err() error {
	return it.err
}

// listResponse is one page as the platform returns it.
type listResponse struct {
	Items      []sandboxDTO `json:"items"`
	NextCursor *string      `json:"nextCursor"`
	TotalCount int          `json:"totalCount"`
}

func (c *Client) fetchPage(ctx context.Context, filter *ListSandboxesQuery, cursor string) ([]*Sandbox, string, error) {
	keys := make([]string, 0, len(filter.Labels))
	for key := range filter.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	labels := make([]string, len(keys))
	for i, key := range keys {
		labels[i] = key + "=" + filter.Labels[key]
	}
	// the platform filters by one state; more are filtered here
	state := ""
	if len(filter.States) == 1 {
		state = string(filter.States[0])
	}
	limit := ""
	if filter.Limit != nil && *filter.Limit > 0 {
		limit = strconv.Itoa(*filter.Limit)
	}

	var page listResponse
	err := c.api.get(ctx, "/sandbox/"+segment(c.namespace),
		query("labels", strings.Join(labels, ","), "state", state, "limit", limit, "cursor", cursor), &page)
	if err != nil {
		return nil, "", err
	}
	items := make([]*Sandbox, 0, len(page.Items))
	for _, dto := range page.Items {
		if len(filter.States) > 1 && !containsState(filter.States, SandboxState(dto.State)) {
			continue
		}
		items = append(items, newSandbox(c.api, dto, types.CodeLanguagePython))
	}
	next := ""
	if page.NextCursor != nil {
		next = *page.NextCursor
	}
	return items, next, nil
}

func containsState(states []SandboxState, state SandboxState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func sandboxPath(namespace, id string) string {
	return "/sandbox/" + segment(namespace) + "/" + segment(id)
}

// ceilSeconds is a duration in whole seconds, rounded up.
func ceilSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
