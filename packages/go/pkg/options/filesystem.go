package options

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
