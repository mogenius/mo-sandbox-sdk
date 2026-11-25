using System.Globalization;

namespace Mogenius.Sandboxes;

/// <summary>
/// A handle to one sandbox: its current data plus the actions on it. The properties read the last known data;
/// <see cref="RefreshDataAsync"/> reloads them from the platform. <see cref="Process"/> runs commands and code
/// inside, <see cref="Fs"/> reads and writes its files.
/// </summary>
public sealed class Sandbox
{
    /// <summary>How often the waits re-read the sandbox.</summary>
    private static readonly TimeSpan PollInterval = TimeSpan.FromSeconds(1);

    /// <summary>Default of the waits.</summary>
    private static readonly TimeSpan DefaultWait = TimeSpan.FromSeconds(60);

    private readonly ApiClient _api;
    private readonly SftpUploads _sftp;
    private SandboxInfo _info;

    internal Sandbox(ApiClient api, SandboxInfo info, CodeLanguage language = CodeLanguage.Python)
    {
        _api = api;
        _info = info;
        Process = new ProcessService(api, info.Namespace, language, PodAsync);
        _sftp = new SftpUploads(api, info.Namespace, PodAsync, GetWorkDirAsync);
        Fs = new FileSystemService(api, info.Namespace, PodAsync, _sftp);
    }

    /// <summary>Commands, code runs and sessions inside the sandbox.</summary>
    public ProcessService Process { get; }

    /// <summary>Files inside the sandbox.</summary>
    public FileSystemService Fs { get; }

    // -----------------------------------------------------------------------------------------------------------------
    // fields
    // -----------------------------------------------------------------------------------------------------------------

    /// <summary>Name of the claim or sandbox object; what every route takes.</summary>
    public string Id => _info.Id;
    /// <summary>Name of the sandbox.</summary>
    public string Name => _info.Name;
    /// <summary>Current state, as of the last read.</summary>
    public SandboxState State => _info.State;
    /// <summary>Why the state is error or still starting.</summary>
    public string? ErrorReason => _info.StateReason;
    /// <summary>Kubernetes labels.</summary>
    public IReadOnlyDictionary<string, string> Labels => _info.Labels;
    /// <summary>Profile the sandbox was made from (snapshot).</summary>
    public string? Snapshot => _info.Profile;
    /// <summary>Image of the sandbox container.</summary>
    public string? Image => _info.Image;
    /// <summary>Kubernetes namespace (target).</summary>
    public string Target => _info.Namespace;
    /// <summary>Kubernetes namespace.</summary>
    public string Namespace => _info.Namespace;
    /// <summary>ISO timestamp of creation.</summary>
    public string? CreatedAt => _info.CreatedAt;
    /// <summary>mogenius: when the sandbox shuts down; null without a deadline.</summary>
    public string? ExpiresAt => _info.ExpiresAt;
    /// <summary>mogenius: pod that commands, files and sessions address.</summary>
    public string? PodName => _info.PodName;
    /// <summary>mogenius: container that commands, files and sessions address.</summary>
    public string ContainerName => _info.ContainerName;
    /// <summary>mogenius: the sandbox's address inside the cluster; null while there is none.</summary>
    public string? ServiceFqdn => _info.ServiceFqdn;
    /// <summary>mogenius: the Sandbox object that runs the pod; null while a claim is unbound.</summary>
    public string? SandboxName => _info.SandboxName;
    /// <summary>mogenius: <c>Running</c> or <c>Suspended</c>.</summary>
    public string? OperatingMode => _info.OperatingMode;
    /// <summary>mogenius: who created the sandbox.</summary>
    public string? CreatedBy => _info.CreatedBy;
    /// <summary>Minutes without a toolbox call after which the sandbox stops; 0 never.</summary>
    public int AutoStopInterval => _info.AutoStopInterval ?? 0;
    /// <summary>mogenius: ISO timestamp of the last toolbox call the cluster saw; null before the first.</summary>
    public string? LastActivityAt => _info.LastActivityAt;
    /// <summary>mogenius: the sandbox has a volume of its own; a stop keeps what lies on it.</summary>
    public bool Persistent => _info.Persistent ?? false;
    /// <summary>Everything the platform knows, as returned.</summary>
    public SandboxInfo Data => _info;

    // -----------------------------------------------------------------------------------------------------------------
    // lifecycle
    // -----------------------------------------------------------------------------------------------------------------

    /// <summary>
    /// Pod and container the pod routes address. A sandbox created without waiting has no pod at first, so one
    /// without is read again before giving up.
    /// </summary>
    private async Task<PodRef> PodAsync(CancellationToken ct)
    {
        if (string.IsNullOrEmpty(_info.PodName))
        {
            await RefreshDataAsync(ct).ConfigureAwait(false);
        }

        var podName = _info.PodName;
        if (string.IsNullOrEmpty(podName))
        {
            throw new MogeniusConflictException($"Sandbox {Id} has no pod yet: wait until it is started.");
        }

        return new PodRef(podName, _info.ContainerName);
    }

    /// <summary>Reloads the sandbox from the platform.</summary>
    public async Task RefreshDataAsync(CancellationToken cancellationToken = default) =>
        _info = await _api.GetAsync<SandboxInfo>(Path(), ct: cancellationToken).ConfigureAwait(false);

    /// <summary>Resumes a stopped sandbox (<c>operatingMode: Running</c>) and waits for it (default 60 s).</summary>
    public async Task StartAsync(TimeSpan? timeout = null, CancellationToken cancellationToken = default)
    {
        _info = await _api.PostAsync<SandboxInfo>($"{Path()}/start", ct: cancellationToken).ConfigureAwait(false);
        await WaitUntilStartedAsync(timeout, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Suspends the sandbox: the pod goes, the volume stays. Waits for it (default 60 s).</summary>
    public async Task StopAsync(TimeSpan? timeout = null, CancellationToken cancellationToken = default)
    {
        _sftp.Close();
        _info = await _api.PostAsync<SandboxInfo>($"{Path()}/stop", ct: cancellationToken).ConfigureAwait(false);
        await WaitUntilStoppedAsync(timeout, cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Deletes the sandbox. With <c>ephemeral</c> (default) pod and storage go with it.</summary>
    public async Task DeleteAsync(CancellationToken cancellationToken = default)
    {
        _sftp.Close();
        _info = await _api.DeleteAsync<SandboxInfo>(Path(), ct: cancellationToken).ConfigureAwait(false) ?? _info;
    }

    /// <summary>Polls until the sandbox is started; throws on error or when the time (default 60 s) runs out.</summary>
    public Task WaitUntilStartedAsync(TimeSpan? timeout = null, CancellationToken cancellationToken = default) =>
        WaitForAsync(SandboxState.Started, timeout ?? DefaultWait, cancellationToken);

    /// <summary>Polls until the sandbox is stopped.</summary>
    public Task WaitUntilStoppedAsync(TimeSpan? timeout = null, CancellationToken cancellationToken = default) =>
        WaitForAsync(SandboxState.Stopped, timeout ?? DefaultWait, cancellationToken);

    /// <summary>Replaces the sandbox's labels (platform labels stay).</summary>
    public async Task<IReadOnlyDictionary<string, string>> SetLabelsAsync(IReadOnlyDictionary<string, string> labels,
        CancellationToken cancellationToken = default)
    {
        _info = await _api.PatchAsync<SandboxInfo>(Path(), new { labels }, cancellationToken).ConfigureAwait(false);
        return _info.Labels;
    }

    /// <summary>Lifetime in minutes from now; 0 removes the deadline.</summary>
    public async Task SetAutoDeleteIntervalAsync(double minutes, CancellationToken cancellationToken = default) =>
        _info = await _api.PatchAsync<SandboxInfo>(Path(), new { ttlMinutes = (int)Math.Max(0, Math.Floor(minutes)) },
            cancellationToken).ConfigureAwait(false);

    /// <summary>Alias of <see cref="SetAutoDeleteIntervalAsync"/>.</summary>
    public Task SetTtlAsync(double minutes, CancellationToken cancellationToken = default) =>
        SetAutoDeleteIntervalAsync(minutes, cancellationToken);

    /// <summary>
    /// Stops the sandbox after that many minutes without a toolbox call (exec, files, sessions, tunnels); the pod
    /// goes, the volume stays. 0 never.
    /// </summary>
    public async Task SetAutostopIntervalAsync(double minutes, CancellationToken cancellationToken = default) =>
        _info = await _api.PatchAsync<SandboxInfo>(Path(),
            new { autoStopInterval = (int)Math.Max(0, Math.Floor(minutes)) }, cancellationToken).ConfigureAwait(false);

    /// <summary>Counts as a toolbox call: the auto-stop interval starts again from now.</summary>
    public async Task RefreshActivityAsync(CancellationToken cancellationToken = default) =>
        _info = await _api.PostAsync<SandboxInfo>($"{Path()}/activity", new { }, ct: cancellationToken)
            .ConfigureAwait(false);

    /// <summary>Home directory of the container user, asked from the running sandbox.</summary>
    public Task<string> GetUserRootDirAsync(CancellationToken cancellationToken = default) =>
        OneLineAsync("printf %s \"$HOME\"", cancellationToken);

    /// <summary>The container's working directory.</summary>
    public Task<string> GetWorkDirAsync(CancellationToken cancellationToken = default) =>
        OneLineAsync("pwd", cancellationToken);

    /// <summary>
    /// mogenius: forwards a local port to <paramref name="port"/> inside the sandbox through the platform's stream
    /// gateway, the way a port-forward does — for an HTTP server, a WebSocket or a database in the sandbox. HTTP,
    /// WebSockets, gRPC and plain TCP all pass; only bytes travel, the sandbox sees no credentials. Needs
    /// <c>OrganizationId</c>, <c>ClusterId</c> and the stream gateway; dispose the tunnel when done.
    /// </summary>
    /// <param name="port">the port inside the sandbox</param>
    /// <param name="localPort">local port to listen on; a free one when null</param>
    /// <param name="cancellationToken">cancels opening the tunnel</param>
    public async Task<Tunnel> TunnelAsync(int port, int? localPort = null,
        CancellationToken cancellationToken = default)
    {
        if (port is < 1 or > 65535)
        {
            throw new MogeniusValidationException($"Port {port} is not a TCP port.");
        }

        var pod = await PodAsync(cancellationToken).ConfigureAwait(false);
        return await Tunnel.OpenAsync(_api, new TunnelTarget(_info.Namespace, pod.PodName, port), localPort ?? 0,
            cancellationToken).ConfigureAwait(false);
    }

    // -----------------------------------------------------------------------------------------------------------------
    // not available on Kubernetes sandboxes
    // -----------------------------------------------------------------------------------------------------------------

    /// <summary>Not supported: mogenius has no archive.</summary>
    public Task SetAutoArchiveIntervalAsync(double minutes) => Task.FromException(
        new MogeniusUnsupportedException("Archiving", "Delete the sandbox, or keep it stopped: the volume stays."));

    /// <summary>Not supported: mogenius has no archive.</summary>
    public Task ArchiveAsync() =>
        Task.FromException(new MogeniusUnsupportedException("Archiving", "Stop the sandbox instead; its volume stays."));

    /// <summary>Not supported: a pod cannot be forked.</summary>
    public Task<Sandbox> ForkAsync() => Task.FromException<Sandbox>(
        new MogeniusUnsupportedException("Forking a running sandbox", "Create a new sandbox from the same profile."));

    // -----------------------------------------------------------------------------------------------------------------
    // helpers
    // -----------------------------------------------------------------------------------------------------------------

    private string Path() => $"/sandbox/{Uri.EscapeDataString(_info.Namespace)}/{Uri.EscapeDataString(_info.Id)}";

    private async Task<string> OneLineAsync(string command, CancellationToken ct)
    {
        var response = await Process.ExecuteCommandAsync(command, cancellationToken: ct).ConfigureAwait(false);
        if (response.ExitCode != 0)
        {
            throw new MogeniusConflictException(
                $"\"{command}\" failed in sandbox {Id} with exit code {response.ExitCode}: {response.Result.Trim()}");
        }

        return (response.Artifacts?.Stdout ?? response.Result).Trim();
    }

    private async Task WaitForAsync(SandboxState target, TimeSpan timeout, CancellationToken ct)
    {
        if (timeout < TimeSpan.Zero)
        {
            timeout = TimeSpan.Zero;
        }

        var deadline = DateTime.UtcNow + timeout;
        while (true)
        {
            if (_info.State == target)
            {
                return;
            }

            if (_info.State == SandboxState.Error)
            {
                throw new MogeniusConflictException(
                    $"Sandbox {Id} is in error state: {_info.StateReason ?? "no reason reported"}",
                    errorCode: "SANDBOX_ERROR");
            }

            if (_info.State is SandboxState.Destroyed or SandboxState.Destroying)
            {
                throw new MogeniusConflictException($"Sandbox {Id} is being destroyed");
            }

            var left = deadline - DateTime.UtcNow;
            if (left <= TimeSpan.Zero)
            {
                throw new MogeniusTimeoutException(
                    $"Sandbox {Id} did not reach state \"{StateName(target)}\" within {timeout.TotalSeconds.ToString(CultureInfo.InvariantCulture)} s (still \"{StateName(_info.State)}\")");
            }

            await Task.Delay(left < PollInterval ? left : PollInterval, ct).ConfigureAwait(false);
            await RefreshDataAsync(ct).ConfigureAwait(false);
        }
    }

    internal static string StateName(SandboxState state) => state.ToString().ToLowerInvariant();
}
