package options

// Transport is how an upload reaches the sandbox (mogenius).
type Transport string

const (
	// TransportHTTP posts the file to the platform API, which hands it to the operator. The default.
	TransportHTTP Transport = "http"
	// TransportSFTP writes the file over SFTP: the stream gateway's port-forward in mode=ssh carries an SSH
	// connection to the operator's SSH gateway, which writes into the pod. The content travels in WebSocket frames,
	// which a web application firewall in front of the API does not inspect — for scripts and source code it would
	// block. Needs the stream gateway (OrganizationID, ClusterID) and the cluster's SSH gateway.
	TransportSFTP Transport = "sftp"
)

// Upload are the options of UploadFile and UploadFiles (mogenius).
type Upload struct {
	// Transport overrides the client's FileTransport for this call.
	Transport Transport
}

// WithTransport sends the upload over transport instead of the client's default.
func WithTransport(transport Transport) func(*Upload) {
	return func(opts *Upload) {
		opts.Transport = transport
	}
}

// CreateFolder are the options of CreateFolder.
type CreateFolder struct {
	// Mode is octal (755) or symbolic (u+x).
	Mode *string
}

func WithMode(mode string) func(*CreateFolder) {
	return func(opts *CreateFolder) {
		opts.Mode = &mode
	}
}

// SetFilePermissions are the options of SetFilePermissions; what is not given stays.
type SetFilePermissions struct {
	// Mode is octal (644) or symbolic (u+x).
	Mode  *string
	Owner *string
	Group *string
}

func WithPermissionMode(mode string) func(*SetFilePermissions) {
	return func(opts *SetFilePermissions) {
		opts.Mode = &mode
	}
}

func WithOwner(owner string) func(*SetFilePermissions) {
	return func(opts *SetFilePermissions) {
		opts.Owner = &owner
	}
}

func WithGroup(group string) func(*SetFilePermissions) {
	return func(opts *SetFilePermissions) {
		opts.Group = &group
	}
}
