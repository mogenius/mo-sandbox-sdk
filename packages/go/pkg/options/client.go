// Package options holds the functional options of the mogenius sandbox SDK's calls.
package options

import "time"

// CreateSandbox are the options of Client.Create.
type CreateSandbox struct {
	// Timeout is how long Create waits for the sandbox to start. Default: 60 s
	Timeout *time.Duration
	// WaitForStart makes Create return only once the sandbox is started. Default: true
	WaitForStart bool
	// LogChannel receives build logs of an image build. mogenius builds no images at
	// create time, so nothing is sent; the channel is closed when Create returns.
	LogChannel chan string
}

func WithTimeout(timeout time.Duration) func(*CreateSandbox) {
	return func(opts *CreateSandbox) {
		opts.Timeout = &timeout
	}
}

func WithWaitForStart(waitForStart bool) func(*CreateSandbox) {
	return func(opts *CreateSandbox) {
		opts.WaitForStart = waitForStart
	}
}

func WithLogChannel(logChannel chan string) func(*CreateSandbox) {
	return func(opts *CreateSandbox) {
		opts.LogChannel = logChannel
	}
}
