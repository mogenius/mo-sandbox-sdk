package mogenius

// SandboxState is a sandbox's state as the platform reports it.
type SandboxState string

const (
	SandboxStateCreating   SandboxState = "creating"
	SandboxStateStarting   SandboxState = "starting"
	SandboxStateStarted    SandboxState = "started"
	SandboxStateStopping   SandboxState = "stopping"
	SandboxStateStopped    SandboxState = "stopped"
	SandboxStateError      SandboxState = "error"
	SandboxStateDestroying SandboxState = "destroying"
	SandboxStateDestroyed  SandboxState = "destroyed"
)
