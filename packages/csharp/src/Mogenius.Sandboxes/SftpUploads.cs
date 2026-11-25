using Renci.SshNet;
using Renci.SshNet.Common;

namespace Mogenius.Sandboxes;

/// <summary>
/// The uploads of one sandbox over SFTP: one session, opened by the first upload and shared by the next ones; it
/// closes after <see cref="IdleTimeout"/> without an upload, when it breaks, and on the sandbox's stop and delete.
/// A session is a port-forward stream in mode=ssh to the operator's SSH gateway, an SSH connection through it (via
/// the tunnel's local listener) and the sftp subsystem on top.
/// </summary>
internal sealed class SftpUploads
{
    /// <summary>How long a session stays open without an upload; tests shorten it.</summary>
    internal static TimeSpan IdleTimeout = TimeSpan.FromSeconds(5);

    /// <summary>The SSH handshake's limit.</summary>
    internal static TimeSpan HandshakeTimeout = TimeSpan.FromSeconds(30);

    private const int ChunkBytes = 64 * 1024;

    private readonly ApiClient _api;
    private readonly string _namespace;
    private readonly Func<CancellationToken, Task<PodRef>> _pod;
    private readonly Func<CancellationToken, Task<string>> _workDir;
    private readonly SemaphoreSlim _opening = new(1, 1);
    private readonly Lock _gate = new();
    private Session? _current;

    public SftpUploads(ApiClient api, string ns, Func<CancellationToken, Task<PodRef>> pod,
        Func<CancellationToken, Task<string>> workDir)
    {
        _api = api;
        _namespace = ns;
        _pod = pod;
        _workDir = workDir;
    }

    /// <summary>
    /// Writes what <paramref name="source"/> yields to <paramref name="path"/>, creating parent folders as the HTTP
    /// upload does, and returns the absolute path. A transfer that fails leaves the remote file as it was: the
    /// session is dropped instead of closing the file, and the gateway discards what it staged.
    /// </summary>
    public async Task<string> UploadAsync(Stream source, string path, IProgress<UploadProgress>? progress,
        CancellationToken ct)
    {
        var session = await AcquireAsync(ct).ConfigureAwait(false);
        var broken = false;
        var target = path;

        MogeniusException Fail(string step, Exception err)
        {
            if (!IsFileError(err))
            {
                broken = true;
                return new MogeniusException(
                    $"Upload to {target} failed: the SFTP connection broke while {step}: {err.Message}",
                    source: ErrorSource.Operator, innerException: err);
            }

            return new MogeniusException($"Upload to {target} failed: {step}: {err.Message}",
                source: ErrorSource.Operator, innerException: err);
        }

        try
        {
            target = await ResolveAsync(session, path, ct).ConfigureAwait(false);
            try
            {
                await Task.Run(() => MkdirAll(session.Client, ParentOf(target)), ct).ConfigureAwait(false);
            }
            catch (Exception err) when (err is not OperationCanceledException)
            {
                throw Fail("creating its folder", err);
            }

            Stream file;
            try
            {
                file = await session.Client.OpenAsync(target, FileMode.Create, FileAccess.Write, ct)
                    .ConfigureAwait(false);
            }
            catch (Exception err) when (err is not OperationCanceledException)
            {
                throw Fail("opening it", err);
            }

            var buffer = new byte[ChunkBytes];
            long position = 0;
            while (true)
            {
                int read;
                try
                {
                    read = await source.ReadAsync(buffer, ct).ConfigureAwait(false);
                }
                catch (Exception err) when (err is not OperationCanceledException)
                {
                    // not a close: that would put what arrived so far over the file
                    broken = true;
                    throw new MogeniusException($"Failed to read the upload's source: {err.Message}",
                        source: ErrorSource.Sdk, innerException: err);
                }

                if (read == 0)
                {
                    break;
                }

                position += read;
                progress?.Report(new UploadProgress(position));
                try
                {
                    await file.WriteAsync(buffer.AsMemory(0, read), ct).ConfigureAwait(false);
                }
                catch (Exception err) when (err is not OperationCanceledException)
                {
                    broken = true;
                    throw Fail("writing it", err);
                }
            }

            try
            {
                // the close commits the file
                await file.DisposeAsync().ConfigureAwait(false);
            }
            catch (Exception err)
            {
                throw Fail("writing it", err);
            }

            return target;
        }
        catch (OperationCanceledException)
        {
            broken = true;
            throw;
        }
        finally
        {
            Release(session, broken);
        }
    }

    /// <summary>Closes the session, if one is open; the sandbox's stop and delete call it.</summary>
    public void Close()
    {
        Session? session;
        lock (_gate)
        {
            session = _current;
        }

        if (session is not null)
        {
            Drop(session);
        }
    }

    private async Task<Session> AcquireAsync(CancellationToken ct)
    {
        var pod = await _pod(ct).ConfigureAwait(false);
        await _opening.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            Session? stale = null;
            lock (_gate)
            {
                if (_current is { } current)
                {
                    if (!current.Closed && current.Pod == pod.PodName)
                    {
                        current.Users++;
                        current.Idle?.Dispose();
                        current.Idle = null;
                        return current;
                    }

                    // the sandbox was restarted, the old pod is gone
                    stale = current;
                }
            }

            if (stale is not null)
            {
                Drop(stale);
            }

            var session = await OpenAsync(pod, ct).ConfigureAwait(false);
            lock (_gate)
            {
                _current = session;
                session.Users++;
            }

            return session;
        }
        finally
        {
            _opening.Release();
        }
    }

    private void Release(Session session, bool broken)
    {
        if (broken)
        {
            lock (_gate)
            {
                session.Users--;
            }

            Drop(session);
            return;
        }

        lock (_gate)
        {
            session.Users--;
            if (session.Users == 0 && !session.Closed)
            {
                session.Idle = new Timer(_ =>
                {
                    bool idle;
                    lock (_gate)
                    {
                        idle = session.Users == 0;
                    }

                    if (idle)
                    {
                        Drop(session);
                    }
                }, null, IdleTimeout, Timeout.InfiniteTimeSpan);
            }
        }
    }

    private void Drop(Session session)
    {
        lock (_gate)
        {
            if (_current == session)
            {
                _current = null;
            }

            if (session.Closed)
            {
                return;
            }

            session.Closed = true;
            session.Idle?.Dispose();
            session.Idle = null;
        }

        try
        {
            session.Client.Dispose();
        }
        catch (Exception)
        {
            // gone already
        }

        _ = session.Tunnel.CloseAsync();
    }

    /// <summary>
    /// Opens the port-forward in mode=ssh (no remote port: the gateway picks the pod's SSH gateway), logs in with
    /// "none" — the stream is authenticated with the API key, the user name picks the container — and starts the
    /// sftp subsystem. The gateway's host key is not pinned: TLS and the key already secure the stream.
    /// </summary>
    private async Task<Session> OpenAsync(PodRef pod, CancellationToken ct)
    {
        Tunnel tunnel;
        try
        {
            tunnel = await Tunnel.OpenAsync(_api, new TunnelTarget(_namespace, pod.PodName, Mode: "ssh"), 0, ct)
                .ConfigureAwait(false);
        }
        catch (Exception err) when (err is not OperationCanceledException)
        {
            throw Unavailable("opening the stream to the SSH gateway", err);
        }

        var info = new ConnectionInfo(tunnel.Host, tunnel.Port, pod.ContainerName,
            new NoneAuthenticationMethod(pod.ContainerName))
        {
            Timeout = HandshakeTimeout,
        };
        var client = new SftpClient(info);
        client.HostKeyReceived += (_, e) => e.CanTrust = true;
        try
        {
            await client.ConnectAsync(ct).ConfigureAwait(false);
        }
        catch (Exception err)
        {
            client.Dispose();
            await tunnel.CloseAsync().ConfigureAwait(false);
            if (err is OperationCanceledException)
            {
                throw;
            }

            throw Unavailable("the SSH gateway refused the connection", err);
        }

        var session = new Session(pod.PodName, tunnel, client);
        // a connection that breaks takes its session with it; the next upload opens a new one
        client.ErrorOccurred += (_, _) => Drop(session);
        _ = tunnel.Completion.ContinueWith(_ => Drop(session), TaskScheduler.Default);
        return session;
    }

    /// <summary>A relative path is relative to the container's working directory, as for the HTTP upload.</summary>
    private async Task<string> ResolveAsync(Session session, string path, CancellationToken ct)
    {
        if (path.StartsWith('/'))
        {
            return Normalize(path);
        }

        session.WorkDir ??= await _workDir(ct).ConfigureAwait(false);
        return Normalize($"{session.WorkDir}/{path}");
    }

    /// <summary>
    /// <paramref name="err"/> as an SFTP_UNAVAILABLE error of the same kind; a missing pod stays as it is — HTTP
    /// would not get further.
    /// </summary>
    private static Exception Unavailable(string what, Exception err) => err switch
    {
        MogeniusNotFoundException => err,
        MogeniusException mogenius => MogeniusErrors.Rewrap(mogenius, $"SFTP: {what}: {mogenius.Message}",
            MogeniusErrorCodes.SftpUnavailable),
        _ => new MogeniusException($"SFTP: {what}: {err.Message}", errorCode: MogeniusErrorCodes.SftpUnavailable,
            source: ErrorSource.Operator, innerException: err),
    };

    /// <summary>Whether <paramref name="err"/> is the server's answer about the file (path, rights, space) rather than a broken connection.</summary>
    private static bool IsFileError(Exception err) => err switch
    {
        SftpPathNotFoundException or SftpPermissionDeniedException => true,
        SshConnectionException or SshOperationTimeoutException or SshAuthenticationException => false,
        SshException => true,
        _ => false,
    };

    private static void MkdirAll(SftpClient client, string dir)
    {
        if (dir == "/")
        {
            return;
        }

        try
        {
            if (client.GetAttributes(dir).IsDirectory)
            {
                return;
            }
        }
        catch (SftpPathNotFoundException)
        {
            // created below
        }

        MkdirAll(client, ParentOf(dir));
        try
        {
            client.CreateDirectory(dir);
        }
        catch (Exception) when (IsDirectory(client, dir))
        {
            // another upload created it meanwhile
        }
    }

    private static bool IsDirectory(SftpClient client, string dir)
    {
        try
        {
            return client.GetAttributes(dir).IsDirectory;
        }
        catch (Exception)
        {
            return false;
        }
    }

    internal static string ParentOf(string path)
    {
        var index = path.LastIndexOf('/');
        return index <= 0 ? "/" : path[..index];
    }

    /// <summary>Resolves <c>.</c> and <c>..</c> and repeated slashes of an absolute path.</summary>
    internal static string Normalize(string path)
    {
        var parts = new List<string>();
        foreach (var part in path.Split('/'))
        {
            if (part == "..")
            {
                if (parts.Count > 0)
                {
                    parts.RemoveAt(parts.Count - 1);
                }
            }
            else if (part.Length > 0 && part != ".")
            {
                parts.Add(part);
            }
        }

        return "/" + string.Join('/', parts);
    }

    private sealed class Session(string pod, Tunnel tunnel, SftpClient client)
    {
        public string Pod { get; } = pod;
        public Tunnel Tunnel { get; } = tunnel;
        public SftpClient Client { get; } = client;
        public string? WorkDir { get; set; }
        public int Users { get; set; }
        public Timer? Idle { get; set; }
        public bool Closed { get; set; }
    }
}
