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

// CodeRun are the options of CodeRun.
type CodeRun struct {
	// Params are the script's arguments and environment.
	Params *types.CodeRunParams
	// Timeout until the run is stopped. Default: 10 s; the platform caps it.
	Timeout *time.Duration
	// Language overrides the sandbox's default language for this run.
	Language types.CodeLanguage
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
