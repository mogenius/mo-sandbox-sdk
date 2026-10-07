// Package types holds the configuration, parameter and result types of the
// mogenius sandbox SDK. Names and shapes follow what agent frameworks expect
// wherever the behaviour is the same, so a ported program keeps compiling
// after the import swap; mogenius-only fields are marked as such.
package types

import (
	"net/http"
	"time"
)

// CodeLanguage is a language CodeRun knows how to launch.
type CodeLanguage string

const (
	CodeLanguagePython     CodeLanguage = "python"
	CodeLanguageJavaScript CodeLanguage = "javascript"
	CodeLanguageTypeScript CodeLanguage = "typescript"
)

// MogeniusConfig is how the SDK reaches the platform. Every empty field falls
// back to an environment variable.
type MogeniusConfig struct {
	// APIKey is a personal access token or API key (mo_pat:…). Env: MOGENIUS_API_KEY.
	APIKey string
	// APIUrl is the platform API base URL. Env: MOGENIUS_API_URL. Default: https://platform-api.mogenius.com
	APIUrl string
	// OrganizationID is the organization the key acts in. Env: MOGENIUS_ORGANIZATION_ID.
	// Optional for keys with one organization scope; streams need it.
	OrganizationID string
	// Target is an alias of Namespace, for programs that pass one.
	Target string

	// mogenius: ClusterID is the cluster the sandboxes run on. Env: MOGENIUS_CLUSTER_ID.
	// Optional for keys with one cluster scope; streams need it.
	ClusterID string
	// mogenius: Namespace is the Kubernetes namespace the sandbox chart puts sandboxes in
	// (sandboxes.namespace.name). Env: MOGENIUS_SANDBOX_NAMESPACE. Default: agent-sandbox
	Namespace string
	// mogenius: WorkspaceName is the workspace to act through when the key has no cluster role.
	// Env: MOGENIUS_WORKSPACE_NAME.
	WorkspaceName string
	// mogenius: StreamURL is the platform's stream gateway, for ExecuteCommandStream and live
	// session logs. Env: MOGENIUS_STREAM_URL. Default: wss://k8s-cmd-stream.mogenius.com
	StreamURL string
	// mogenius: HTTPClient replaces http.DefaultClient for API calls, downloads and streams.
	HTTPClient *http.Client
}

// SandboxBaseParams are the parameters every Create call shares.
type SandboxBaseParams struct {
	// Name of the sandbox; generated from the profile when empty.
	Name string
	// Language is the default for CodeRun. Default: python
	Language CodeLanguage
	// EnvVars for the sandbox container. On a warm-pool sandbox the profile must allow env injection.
	EnvVars map[string]string
	// Labels are Kubernetes labels; List filters by them.
	Labels map[string]string
	// AutoStopInterval and AutoArchiveInterval are accepted for compatibility: mogenius has
	// no idle timer and no archive, so they are not used.
	AutoStopInterval    *int
	AutoArchiveInterval *int
	// AutoDeleteInterval is the sandbox's lifetime in minutes from its creation; nil or
	// less than 1 means no deadline.
	AutoDeleteInterval *int
	// Ephemeral: pod and storage go when the lifetime ends or the sandbox is deleted. That is
	// the platform's default, so false keeps it too.
	Ephemeral bool
}

// SnapshotParams create a sandbox from a profile (snapshot): claimed from the
// profile's warm pool, ready in seconds.
type SnapshotParams struct {
	SandboxBaseParams
	// Snapshot is the profile: SandboxTemplate and SandboxWarmPool of that name. Default: default
	Snapshot string
}

// ImageParams create a sandbox from an image: a pod stamped from the profile's
// template with this image. A pod start, not a warm-pool claim.
type ImageParams struct {
	SandboxBaseParams
	// Image is the container image, e.g. python:3.12-slim.
	Image string
	// mogenius: Snapshot is the profile whose pod template is used around the image. Default: default
	Snapshot string
}

// FileInfo is one file or folder.
type FileInfo struct {
	Name string
	Size int64
	// Mode is octal, e.g. 0644.
	Mode         string
	ModifiedTime time.Time
	IsDirectory  bool

	// mogenius: Path is the absolute path inside the container.
	Path string
	// mogenius: Permissions in ls -l style, e.g. -rw-r--r--.
	Permissions string
	// mogenius
	Owner string
	// mogenius
	Group string
	// mogenius: MimeType is the sniffed media type of a regular file; empty for folders.
	MimeType string
}

// CodeRunParams are the arguments and environment of a CodeRun.
type CodeRunParams struct {
	// Argv is passed to the script (sys.argv[1:], process.argv[2:]).
	Argv []string
	Env  map[string]string
}

// ExecuteResponse is the outcome of ExecuteCommand and CodeRun.
type ExecuteResponse struct {
	// ExitCode of the command; 0 means success.
	ExitCode int
	// Result is the combined output: stdout followed by stderr.
	Result    string
	Artifacts *ExecutionArtifacts
}

// ExecutionArtifacts keep the output streams apart.
type ExecutionArtifacts struct {
	Stdout string
	// Charts matplotlib printed as artifacts; filled by CodeRun for Python.
	Charts []Chart
	// mogenius: Stderr kept apart from Stdout.
	Stderr string
}

// Chart is a chart CodeRun extracted from a Python run's output.
type Chart struct {
	Type  *string `json:"type,omitempty"`
	Title *string `json:"title,omitempty"`
	// Png is the rendered chart, base64-encoded.
	Png      *string          `json:"png,omitempty"`
	Elements []map[string]any `json:"elements,omitempty"`
}

// ExecEventType tells what an ExecEvent carries.
type ExecEventType string

const (
	ExecEventStdout ExecEventType = "stdout"
	ExecEventStderr ExecEventType = "stderr"
	ExecEventExit   ExecEventType = "exit"
)

// ExecEvent is one event of ExecuteCommandStream (mogenius). Output arrives
// as bytes of the stream it was written to, in order within each stream; the
// last event is always the exit.
type ExecEvent struct {
	Type ExecEventType
	// Data is the output of a stdout or stderr event.
	Data []byte
	// ExitCode of the command (exit event); 137 when it was killed, which a timeout does.
	ExitCode int
	// Truncated: a stream exceeded the cluster's output cap and the rest was dropped (exit event).
	Truncated bool
	// TimedOut: the command was stopped at its timeout (exit event).
	TimedOut bool
}

// SessionCommandLogsResponse is what a session command has printed so far.
type SessionCommandLogsResponse struct {
	// Output is stdout and stderr in arrival order: a session's shell writes both into one stream.
	Output string
}
