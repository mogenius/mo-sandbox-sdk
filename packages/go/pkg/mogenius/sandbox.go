package mogenius

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// defaultWaitTimeout is how long Start and Stop wait by default.
const defaultWaitTimeout = 60 * time.Second

// pollInterval is how often the WaitFor… methods re-read the sandbox; tests shorten it.
var pollInterval = time.Second

// Sandbox is a handle to one sandbox: its data as of the last call, plus the
// actions on it. RefreshData reloads the fields; Process runs commands and
// code inside, FileSystem reads and writes its files. A Sandbox is not safe
// for concurrent use while its fields change (RefreshData, Start, Stop, …).
type Sandbox struct {
	// ID is the name of the claim or sandbox object; what every route takes.
	ID   string
	Name string
	// Snapshot is the profile the sandbox was made from.
	Snapshot *string
	Labels   map[string]string
	// Target is the Kubernetes namespace (same as Namespace).
	Target string
	State  SandboxState
	// ErrorReason is why the state is error or still starting.
	ErrorReason *string
	CreatedAt   *string

	// mogenius: Namespace is the Kubernetes namespace.
	Namespace string
	// mogenius: Image the sandbox runs.
	Image *string
	// mogenius: PodName and ContainerName are what commands, files and sessions address; no pod while creating.
	PodName       *string
	ContainerName string
	// mogenius: ExpiresAt is when the sandbox shuts down; nil without a deadline.
	ExpiresAt *string
	// mogenius: Ephemeral: pod and storage go with the sandbox.
	Ephemeral bool
	// mogenius: Kind is SandboxClaim (from a warm pool) or Sandbox (own image).
	Kind string
	// mogenius: ServiceFQDN is the sandbox's address inside the cluster; nil while there is none.
	ServiceFQDN *string
	// mogenius: SandboxName is the Sandbox object that runs the pod; nil while a claim is unbound.
	SandboxName *string
	// mogenius: OperatingMode is Running or Suspended.
	OperatingMode *string
	// mogenius: CreatedBy is who created the sandbox.
	CreatedBy *string

	// Process runs commands, code and sessions inside the sandbox.
	Process *ProcessService
	// FileSystem reads and writes the sandbox's files.
	FileSystem *FileSystemService

	api      *apiClient
	language types.CodeLanguage
}

// sandboxDTO is the sandbox as the platform returns it.
type sandboxDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Namespace     string            `json:"namespace"`
	Kind          string            `json:"kind"`
	State         string            `json:"state"`
	StateReason   *string           `json:"stateReason"`
	Labels        map[string]string `json:"labels"`
	Profile       *string           `json:"profile"`
	Image         *string           `json:"image"`
	PodName       *string           `json:"podName"`
	ContainerName string            `json:"containerName"`
	Ephemeral     bool              `json:"ephemeral"`
	ExpiresAt     *string           `json:"expiresAt"`
	CreatedAt     *string           `json:"createdAt"`
	ServiceFQDN   *string           `json:"serviceFQDN"`
	SandboxName   *string           `json:"sandboxName"`
	OperatingMode *string           `json:"operatingMode"`
	CreatedBy     *string           `json:"createdBy"`
}

func newSandbox(api *apiClient, dto sandboxDTO, language types.CodeLanguage) *Sandbox {
	sandbox := &Sandbox{api: api, language: language}
	sandbox.Process = &ProcessService{sandbox: sandbox}
	sandbox.FileSystem = &FileSystemService{sandbox: sandbox, resumeDelays: defaultResumeDelays}
	sandbox.apply(dto)
	return sandbox
}

func (s *Sandbox) apply(dto sandboxDTO) {
	labels := dto.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	s.ID, s.Name, s.Snapshot, s.Labels = dto.ID, dto.Name, dto.Profile, labels
	s.Target, s.Namespace, s.State, s.ErrorReason = dto.Namespace, dto.Namespace, SandboxState(dto.State), dto.StateReason
	s.CreatedAt, s.Image, s.PodName, s.ContainerName = dto.CreatedAt, dto.Image, dto.PodName, dto.ContainerName
	s.ExpiresAt, s.Ephemeral, s.Kind = dto.ExpiresAt, dto.Ephemeral, dto.Kind
	s.ServiceFQDN, s.SandboxName, s.OperatingMode, s.CreatedBy = dto.ServiceFQDN, dto.SandboxName, dto.OperatingMode, dto.CreatedBy
}

func (s *Sandbox) path() string {
	return sandboxPath(s.Namespace, s.ID)
}

// podName is the sandbox's pod, empty while there is none.
func (s *Sandbox) podName() string {
	if s.PodName == nil {
		return ""
	}
	return *s.PodName
}

// pod is the pod and container the pod routes address. A sandbox created
// without waiting has no pod at first, so one without is read again before
// giving up.
func (s *Sandbox) pod(ctx context.Context) (podName, container string, err error) {
	if s.podName() == "" {
		if err := s.RefreshData(ctx); err != nil {
			return "", "", err
		}
	}
	if s.podName() == "" {
		return "", "", sdkConflict(fmt.Sprintf("Sandbox %s has no pod yet: wait until it is started.", s.ID))
	}
	return s.podName(), s.ContainerName, nil
}

// RefreshData reloads the sandbox from the platform.
func (s *Sandbox) RefreshData(ctx context.Context) error {
	return s.call(ctx, func(dto *sandboxDTO) error { return s.api.get(ctx, s.path(), nil, dto) })
}

// Start resumes a stopped sandbox (operatingMode Running) and waits up to 60 s for it.
func (s *Sandbox) Start(ctx context.Context) error {
	return s.StartWithTimeout(ctx, defaultWaitTimeout)
}

// StartWithTimeout is Start with its own wait; 0 waits as long as ctx allows.
func (s *Sandbox) StartWithTimeout(ctx context.Context, timeout time.Duration) error {
	if err := s.call(ctx, func(dto *sandboxDTO) error { return s.api.post(ctx, s.path()+"/start", nil, nil, dto) }); err != nil {
		return err
	}
	return s.WaitForStart(ctx, timeout)
}

// Stop suspends the sandbox: the pod goes, the volume stays. Waits up to 60 s.
func (s *Sandbox) Stop(ctx context.Context) error {
	return s.StopWithTimeout(ctx, defaultWaitTimeout, false)
}

// StopWithTimeout is Stop with its own wait; 0 waits as long as ctx allows.
// force is accepted for compatibility: Kubernetes stops the pod the same way either way.
func (s *Sandbox) StopWithTimeout(ctx context.Context, timeout time.Duration, force bool) error {
	if err := s.call(ctx, func(dto *sandboxDTO) error { return s.api.post(ctx, s.path()+"/stop", nil, nil, dto) }); err != nil {
		return err
	}
	return s.WaitForStop(ctx, timeout)
}

// Delete deletes the sandbox; pod and storage go with it.
func (s *Sandbox) Delete(ctx context.Context) error {
	return s.call(ctx, func(dto *sandboxDTO) error { return s.api.delete(ctx, s.path(), nil, dto) })
}

// DeleteWithTimeout is Delete bounded by timeout; 0 is as long as ctx allows.
func (s *Sandbox) DeleteWithTimeout(ctx context.Context, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return s.Delete(ctx)
}

// WaitForStart polls until the sandbox is started; it fails on the error
// state and when timeout runs out (0: as long as ctx allows).
func (s *Sandbox) WaitForStart(ctx context.Context, timeout time.Duration) error {
	return s.waitFor(ctx, SandboxStateStarted, timeout)
}

// WaitForStop polls until the sandbox is stopped.
func (s *Sandbox) WaitForStop(ctx context.Context, timeout time.Duration) error {
	return s.waitFor(ctx, SandboxStateStopped, timeout)
}

// SetLabels replaces the sandbox's labels (platform labels stay).
func (s *Sandbox) SetLabels(ctx context.Context, labels map[string]string) error {
	return s.call(ctx, func(dto *sandboxDTO) error {
		return s.api.patch(ctx, s.path(), map[string]any{"labels": labels}, dto)
	})
}

// SetAutoDeleteInterval sets the sandbox's lifetime in minutes from now.
// nil, 0 or a negative value removes the deadline.
func (s *Sandbox) SetAutoDeleteInterval(ctx context.Context, intervalMinutes *int) error {
	minutes := 0
	if intervalMinutes != nil && *intervalMinutes > 0 {
		minutes = *intervalMinutes
	}
	return s.call(ctx, func(dto *sandboxDTO) error {
		return s.api.patch(ctx, s.path(), map[string]any{"ttlMinutes": minutes}, dto)
	})
}

// GetUserHomeDir is the home directory of the container user, asked from the running sandbox.
func (s *Sandbox) GetUserHomeDir(ctx context.Context) (string, error) {
	return s.oneLine(ctx, `printf %s "$HOME"`)
}

// GetWorkingDir is the container's working directory.
func (s *Sandbox) GetWorkingDir(ctx context.Context) (string, error) {
	return s.oneLine(ctx, "pwd")
}

// SetAutoArchiveInterval is not available: mogenius has no archive.
func (s *Sandbox) SetAutoArchiveInterval(ctx context.Context, intervalMinutes *int) error {
	return sdkerrors.NewMogeniusUnsupportedError("Archiving", "Delete the sandbox, or keep it stopped: the volume stays.")
}

// Archive is not available: stop the sandbox instead, its volume stays.
func (s *Sandbox) Archive(ctx context.Context) error {
	return sdkerrors.NewMogeniusUnsupportedError("Archiving", "Stop the sandbox instead; its volume stays.")
}

// Pause is not available: stop the sandbox instead.
func (s *Sandbox) Pause(ctx context.Context) error {
	return sdkerrors.NewMogeniusUnsupportedError("Pausing a running sandbox", "Stop the sandbox instead: the pod goes, the volume stays.")
}

// ExperimentalFork is not available: create a new sandbox from the same profile.
func (s *Sandbox) ExperimentalFork(ctx context.Context, name *string) (*Sandbox, error) {
	return nil, sdkerrors.NewMogeniusUnsupportedError("Forking a running sandbox", "Create a new sandbox from the same profile.")
}

// ExperimentalCreateSnapshot is not available: profiles are configured on the cluster's Sandboxes page.
func (s *Sandbox) ExperimentalCreateSnapshot(ctx context.Context, name string) error {
	return sdkerrors.NewMogeniusUnsupportedError("Snapshots of a running sandbox",
		"Profiles are configured on the cluster's Sandboxes page; pass the profile name as Snapshot to Create.")
}

// call runs a request that answers with the sandbox and takes over its data.
func (s *Sandbox) call(ctx context.Context, request func(dto *sandboxDTO) error) error {
	var dto sandboxDTO
	if err := request(&dto); err != nil {
		return err
	}
	// a route without a body (204) leaves the data as it was
	if dto.ID != "" {
		s.apply(dto)
	}
	return nil
}

func (s *Sandbox) oneLine(ctx context.Context, command string) (string, error) {
	response, err := s.Process.ExecuteCommand(ctx, command)
	if err != nil {
		return "", err
	}
	if response.ExitCode != 0 {
		return "", sdkConflict(fmt.Sprintf("%q failed in sandbox %s with exit code %d: %s",
			command, s.ID, response.ExitCode, strings.TrimSpace(response.Result)))
	}
	output := response.Result
	if response.Artifacts != nil {
		output = response.Artifacts.Stdout
	}
	return strings.TrimSpace(output), nil
}

func (s *Sandbox) waitFor(ctx context.Context, target SandboxState, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		switch s.State {
		case target:
			return nil
		case SandboxStateError:
			reason := "no reason reported"
			if s.ErrorReason != nil {
				reason = *s.ErrorReason
			}
			conflict := sdkConflict(fmt.Sprintf("Sandbox %s is in error state: %s", s.ID, reason))
			conflict.ErrorCode = "SANDBOX_ERROR"
			return conflict
		case SandboxStateDestroying, SandboxStateDestroyed:
			return sdkConflict(fmt.Sprintf("Sandbox %s is being destroyed", s.ID))
		}
		wait := pollInterval
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				return sdkerrors.NewMogeniusTimeoutError(fmt.Sprintf("Sandbox %s did not reach state %q within %s (still %q)",
					s.ID, target, timeout, s.State))
			}
			wait = min(wait, left)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for sandbox %s: %w", s.ID, ctx.Err())
		case <-timer.C:
		}
		if err := s.RefreshData(ctx); err != nil {
			return err
		}
	}
}
