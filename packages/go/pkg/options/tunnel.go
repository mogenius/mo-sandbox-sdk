package options

// Tunnel are the options of Sandbox.Tunnel.
type Tunnel struct {
	// LocalPort is the port on 127.0.0.1 the tunnel listens on; 0 picks a free one.
	LocalPort int
}

func WithLocalPort(port int) func(*Tunnel) {
	return func(opts *Tunnel) {
		opts.LocalPort = port
	}
}
