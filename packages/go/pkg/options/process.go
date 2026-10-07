package options

import (
	"time"

	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// ExecuteCommand are the options of ExecuteCommand and ExecuteCommandStream.
type ExecuteCommand struct {
	// Cwd is the working directory inside the container; the container's own when nil.
	Cwd *string
	// Env are environment variables for this run; keys must be shell identifiers.
	Env map[string]string
	// Timeout until the command is stopped. Default: 10 s; the platform caps it.
	Timeout *time.Duration
	// mogenius: Container to run in; the sandbox's own when nil.
	Container *string
}

func WithCwd(cwd string) func(*ExecuteCommand) {
	return func(opts *ExecuteCommand) {
		opts.Cwd = &cwd
	}
}

func WithCommandEnv(env map[string]string) func(*ExecuteCommand) {
	return func(opts *ExecuteCommand) {
		opts.Env = env
	}
}

func WithExecuteTimeout(timeout time.Duration) func(*ExecuteCommand) {
	return func(opts *ExecuteCommand) {
		opts.Timeout = &timeout
	}
}

func WithContainer(container string) func(*ExecuteCommand) {
	return func(opts *ExecuteCommand) {
		opts.Container = &container
	}
}

// CodeRun are the options of CodeRun.
type CodeRun struct {
	// Params are the script's arguments and environment.
	Params *types.CodeRunParams
	// Timeout until the run is stopped. Default: 10 s; the platform caps it.
	Timeout *time.Duration
	// Language overrides the sandbox's default language for this run.
	Language types.CodeLanguage
	// mogenius: Container to run in; the sandbox's own when nil.
	Container *string
}

func WithCodeRunParams(params types.CodeRunParams) func(*CodeRun) {
	return func(opts *CodeRun) {
		opts.Params = &params
	}
}

func WithCodeRunLanguage(language types.CodeLanguage) func(*CodeRun) {
	return func(opts *CodeRun) {
		opts.Language = language
	}
}

func WithCodeRunTimeout(timeout time.Duration) func(*CodeRun) {
	return func(opts *CodeRun) {
		opts.Timeout = &timeout
	}
}

func WithCodeRunContainer(container string) func(*CodeRun) {
	return func(opts *CodeRun) {
		opts.Container = &container
	}
}

// CreateSession are the options of CreateSession.
type CreateSession struct {
	// mogenius: Container the session's shell runs in; the sandbox's own when nil.
	Container *string
}

func WithSessionContainer(container string) func(*CreateSession) {
	return func(opts *CreateSession) {
		opts.Container = &container
	}
}
