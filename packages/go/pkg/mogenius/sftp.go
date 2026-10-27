package mogenius

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

// ErrorCodeSFTPUnavailable marks an upload over SFTP that found no way into the sandbox: the stream gateway or the
// cluster's SSH gateway refused the connection, cannot do mode=ssh or is switched off. Nothing was written; the
// caller decides whether to upload over HTTP instead (mogenius).
const ErrorCodeSFTPUnavailable = "SFTP_UNAVAILABLE"

const sftpHandshakeTimeout = 30 * time.Second

// sftpIdle is how long a sandbox's SFTP session stays open without an upload; tests shorten it.
var sftpIdle = 5 * time.Second

// sftpSession is a sandbox's SFTP connection: a port-forward stream in mode=ssh to the operator's SSH gateway, an
// SSH connection through it and the sftp subsystem on top. One per sandbox, shared by its uploads.
type sftpSession struct {
	pod     string
	tunnel  *Tunnel
	ssh     *ssh.Client
	client  *sftp.Client
	workDir string
	users   int
	idle    *time.Timer
}

func (sess *sftpSession) close() {
	_ = sess.client.Close()
	_ = sess.ssh.Close()
	_ = sess.tunnel.Close()
}

// acquireSFTP hands out the sandbox's SFTP session, opening it on first use; release ends the use, broken says the
// connection failed and the session must go.
func (s *Sandbox) acquireSFTP(ctx context.Context) (sess *sftpSession, release func(broken bool), err error) {
	podName, container, err := s.pod(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.sftpMu.Lock()
	defer s.sftpMu.Unlock()
	if s.sftpSess != nil && s.sftpSess.pod != podName {
		// the sandbox was restarted: the old pod is gone
		s.dropSFTP(s.sftpSess)
	}
	if s.sftpSess == nil {
		if s.sftpSess, err = s.openSFTP(ctx, podName, container); err != nil {
			return nil, nil, err
		}
	}
	sess = s.sftpSess
	sess.users++
	if sess.idle != nil {
		sess.idle.Stop()
	}
	return sess, func(broken bool) {
		s.sftpMu.Lock()
		defer s.sftpMu.Unlock()
		sess.users--
		switch {
		case broken:
			s.dropSFTP(sess)
		case sess.users == 0 && s.sftpSess == sess:
			sess.idle = time.AfterFunc(sftpIdle, func() {
				s.sftpMu.Lock()
				defer s.sftpMu.Unlock()
				if sess.users == 0 {
					s.dropSFTP(sess)
				}
			})
		}
	}, nil
}

// dropSFTP closes sess and forgets it when it is the sandbox's; the caller holds sftpMu.
func (s *Sandbox) dropSFTP(sess *sftpSession) {
	if s.sftpSess == sess {
		s.sftpSess = nil
	}
	if sess.idle != nil {
		sess.idle.Stop()
	}
	sess.close()
}

// closeSFTP closes the sandbox's SFTP session, if one is open; Stop and Delete call it, the pod goes.
func (s *Sandbox) closeSFTP() {
	s.sftpMu.Lock()
	defer s.sftpMu.Unlock()
	if s.sftpSess != nil {
		s.dropSFTP(s.sftpSess)
	}
}

// openSFTP opens the port-forward in mode=ssh (no remotePort: the gateway picks the pod's SSH gateway), logs in to
// the SSH gateway with "none" — the stream is authenticated with the API key, the user name picks the container —
// and starts the sftp subsystem. The gateway's host key is not pinned: TLS and the key already secure the stream.
func (s *Sandbox) openSFTP(ctx context.Context, podName, container string) (*sftpSession, error) {
	tunnel, err := s.openTunnel(ctx, map[string]string{
		"type":         "PORT_FORWARD",
		"cmd":          "port-forward",
		"namespace":    s.Namespace,
		"kind":         "Pod",
		"workloadName": podName,
		"mode":         "ssh",
	}, nil)
	if err != nil {
		return nil, sftpUnavailable(ctx, "opening the stream to the SSH gateway", err)
	}
	conn, err := tunnel.DialContext(ctx, "tcp", "")
	if err != nil {
		_ = tunnel.Close()
		return nil, sftpUnavailable(ctx, "opening the stream to the SSH gateway", err)
	}
	deadline := time.Now().Add(sftpHandshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, podName, &ssh.ClientConfig{
		User:            container,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	stop()
	if err != nil {
		_ = conn.Close()
		_ = tunnel.Close()
		return nil, sftpUnavailable(ctx, "the SSH gateway refused the connection", err)
	}
	_ = conn.SetDeadline(time.Time{})
	sshClient := ssh.NewClient(sshConn, chans, reqs)
	client, err := sftp.NewClient(sshClient, sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = sshClient.Close()
		_ = tunnel.Close()
		return nil, sftpUnavailable(ctx, "the SSH gateway refused the sftp subsystem", err)
	}
	sess := &sftpSession{pod: podName, tunnel: tunnel, ssh: sshClient, client: client}
	go func() {
		// a connection that breaks takes its session with it; the next upload opens a new one
		_ = sshClient.Wait()
		s.sftpMu.Lock()
		defer s.sftpMu.Unlock()
		if s.sftpSess == sess {
			s.dropSFTP(sess)
		}
	}()
	return sess, nil
}

// sftpUnavailable is err as an SFTP_UNAVAILABLE error, keeping its kind; a cancelled context and a missing pod stay
// as they are — HTTP would not get further.
func sftpUnavailable(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var notFound *sdkerrors.MogeniusNotFoundError
	if errors.As(err, &notFound) {
		return err
	}
	var base *sdkerrors.MogeniusError
	if errors.As(err, &base) {
		base.ErrorCode = ErrorCodeSFTPUnavailable
		base.Message = "SFTP: " + what + ": " + base.Message
		return err
	}
	failure := sdkerrors.NewMogeniusError(fmt.Sprintf("SFTP: %s: %v", what, err), 0, nil)
	failure.ErrorCode = ErrorCodeSFTPUnavailable
	failure.Source = sdkerrors.SourceOperator
	return failure
}

// uploadSFTP writes content to destination over the sandbox's SFTP session, creating parent folders as the HTTP
// upload does, and returns the absolute path. A transfer that fails leaves the remote file as it was: the session
// is dropped instead of closing the file, and the gateway discards what it staged.
func (f *FileSystemService) uploadSFTP(ctx context.Context, content io.Reader, size int64, destination string, progress func(UploadProgress)) (string, error) {
	sess, release, err := f.sandbox.acquireSFTP(ctx)
	if err != nil {
		return destination, err
	}
	broken := false
	defer func() { release(broken) }()
	target, err := f.sftpPath(ctx, sess, destination)
	if err != nil {
		return destination, err
	}
	fail := func(step string, err error) error {
		if ctx.Err() != nil {
			broken = true
			return ctx.Err()
		}
		if !sftpFileError(err) {
			broken = true
			failure := sdkerrors.NewMogeniusError(fmt.Sprintf("Upload to %s failed: the SFTP connection broke while %s: %v", target, step, err), 0, nil)
			failure.Source = sdkerrors.SourceOperator
			return failure
		}
		failure := sdkerrors.NewMogeniusError(fmt.Sprintf("Upload to %s failed: %s: %v", target, step, err), 0, nil)
		failure.Source = sdkerrors.SourceOperator
		return failure
	}
	if err := sess.client.MkdirAll(path.Dir(target)); err != nil {
		return target, fail("creating its folder", err)
	}
	file, err := sess.client.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return target, fail("opening it", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = sess.ssh.Close() })
	defer stop()
	reader := &uploadReader{ctx: ctx, reader: content, size: size, report: progress}
	if _, err := io.Copy(file, reader); err != nil {
		// not file.Close(): that would put what arrived so far over the file
		broken = true
		if reader.err != nil {
			return target, reader.err
		}
		return target, fail("writing it", err)
	}
	if err := file.Close(); err != nil {
		return target, fail("writing it", err)
	}
	return target, nil
}

// sftpPath makes destination absolute: a relative path is relative to the container's working directory, as for
// the HTTP upload (the SSH gateway would start in the home directory).
func (f *FileSystemService) sftpPath(ctx context.Context, sess *sftpSession, destination string) (string, error) {
	if path.IsAbs(destination) {
		return path.Clean(destination), nil
	}
	f.sandbox.sftpMu.Lock()
	dir := sess.workDir
	f.sandbox.sftpMu.Unlock()
	if dir == "" {
		var err error
		if dir, err = f.sandbox.GetWorkingDir(ctx); err != nil {
			return destination, err
		}
		f.sandbox.sftpMu.Lock()
		sess.workDir = dir
		f.sandbox.sftpMu.Unlock()
	}
	return path.Join(dir, destination), nil
}

// sftpFileError is whether err is the server's answer about the file (path, rights, space) rather than a broken
// connection.
func sftpFileError(err error) bool {
	var status *sftp.StatusError
	return errors.As(err, &status) || errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrExist)
}

// uploadSFTPFiles writes files one after the other over the same session, one result each.
func (f *FileSystemService) uploadSFTPFiles(ctx context.Context, files []types.FileUpload) ([]types.UploadResult, error) {
	results := make([]types.UploadResult, len(files))
	for i, file := range files {
		content, size, closeSource, err := openUploadSource("UploadFiles", file.Source)
		if err != nil {
			return nil, err
		}
		target, err := f.uploadSFTP(ctx, content, size, file.Destination, nil)
		closeSource()
		if err != nil {
			var base *sdkerrors.MogeniusError
			if ctx.Err() != nil || (errors.As(err, &base) && base.ErrorCode == ErrorCodeSFTPUnavailable && i == 0) {
				// nothing can land: the call fails as the HTTP upload's request would
				return nil, err
			}
			results[i] = types.UploadResult{Path: target, Error: err.Error()}
			continue
		}
		results[i] = types.UploadResult{Path: target, Success: true}
	}
	return results, nil
}

// uploadReader feeds an SFTP upload: it stops on a cancelled context, reports progress and tells pkg/sftp the size
// (-1 unknown) so it writes with several requests in flight.
type uploadReader struct {
	ctx    context.Context
	reader io.Reader
	size   int64
	total  int64
	report func(UploadProgress)
	err    error
}

func (u *uploadReader) Size() int64 { return u.size }

func (u *uploadReader) Read(p []byte) (int, error) {
	if err := u.ctx.Err(); err != nil {
		u.err = err
		return 0, err
	}
	n, err := u.reader.Read(p)
	if n > 0 {
		u.total += int64(n)
		if u.report != nil {
			u.report(UploadProgress{BytesSent: u.total})
		}
	}
	if err != nil && err != io.EOF {
		u.err = sdkerrors.NewMogeniusError(fmt.Sprintf("Failed to read the upload's source: %v", err), 0, nil)
	}
	return n, err
}
