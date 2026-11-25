using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Mogenius.Sandboxes;

// Public types of Mogenius.Sandboxes. Names and shapes follow what agent frameworks expect wherever the behaviour is
// the same; mogenius-only fields are marked as such.

/// <summary>Languages <c>CodeRunAsync</c> knows how to launch.</summary>
public enum CodeLanguage
{
    /// <summary>Through <c>python3</c>; matplotlib figures come back as charts.</summary>
    Python,
    /// <summary>Through <c>tsx</c>.</summary>
    TypeScript,
    /// <summary>Through <c>node</c>.</summary>
    JavaScript,
}

/// <summary>Sandbox states as the platform reports them.</summary>
[JsonConverter(typeof(JsonStringEnumConverter<SandboxState>))]
public enum SandboxState
{
    /// <summary>creating</summary>
    [JsonStringEnumMemberName("creating")] Creating,
    /// <summary>starting</summary>
    [JsonStringEnumMemberName("starting")] Starting,
    /// <summary>started</summary>
    [JsonStringEnumMemberName("started")] Started,
    /// <summary>stopping</summary>
    [JsonStringEnumMemberName("stopping")] Stopping,
    /// <summary>stopped</summary>
    [JsonStringEnumMemberName("stopped")] Stopped,
    /// <summary>error</summary>
    [JsonStringEnumMemberName("error")] Error,
    /// <summary>destroying</summary>
    [JsonStringEnumMemberName("destroying")] Destroying,
    /// <summary>destroyed</summary>
    [JsonStringEnumMemberName("destroyed")] Destroyed,
}

/// <summary>
/// mogenius: how an upload reaches the sandbox. <see cref="Http"/> posts it to the platform API; <see cref="Sftp"/>
/// writes it over SFTP through the stream gateway's port-forward in mode=ssh to the cluster's SSH gateway —
/// WebSocket frames, which a web application firewall in front of the API does not inspect.
/// </summary>
public enum FileTransport
{
    /// <summary>The platform API.</summary>
    Http,
    /// <summary>SFTP through the stream gateway.</summary>
    Sftp,
}

/// <summary>Parameters of <c>CreateAsync()</c>.</summary>
public sealed class CreateSandboxParams
{
    /// <summary>Name of the sandbox; generated from the profile when absent.</summary>
    public string? Name { get; init; }

    /// <summary>
    /// Profile = SandboxTemplate and SandboxWarmPool of that name; with <see cref="Image"/> the profile whose pod
    /// template is used around the image. Default: the configured profile, otherwise <c>default</c>.
    /// </summary>
    public string? Snapshot { get; init; }

    /// <summary>Create from an image: a Sandbox stamped from the profile's template with this image. A pod start.</summary>
    public string? Image { get; init; }

    /// <summary>Default language for <c>CodeRunAsync</c>. Default <see cref="CodeLanguage.Python"/>.</summary>
    public CodeLanguage? Language { get; init; }

    /// <summary>Kubernetes labels; <c>ListAsync()</c> and <c>FindOneAsync()</c> filter by them.</summary>
    public IReadOnlyDictionary<string, string>? Labels { get; init; }

    /// <summary>Environment for the sandbox container. On a warm-pool sandbox the profile must allow env injection.</summary>
    public IReadOnlyDictionary<string, string>? EnvVars { get; init; }

    /// <summary>Minutes until the sandbox is shut down; absent means no deadline.</summary>
    public double? AutoDeleteInterval { get; init; }

    /// <summary>Delete pod and storage when the lifetime ends or the sandbox is deleted (default true).</summary>
    public bool? Ephemeral { get; init; }

    /// <summary>
    /// Minutes without a toolbox call (exec, files, sessions, tunnels) after which the sandbox stops: the pod goes,
    /// the volume stays, <c>StartAsync()</c> brings it back. Absent or 0: never.
    /// </summary>
    public double? AutoStopInterval { get; init; }

    /// <summary>Accepted for compatibility; mogenius has no archive.</summary>
    public double? AutoArchiveInterval { get; init; }
}

/// <summary>Filter of <c>FindOneAsync()</c>.</summary>
public sealed record SandboxFilter(
    string? Id = null,
    IReadOnlyDictionary<string, string>? Labels = null,
    SandboxState? State = null);

/// <summary>One page of <c>ListAsync()</c>.</summary>
/// <param name="Items">the sandboxes of this page</param>
/// <param name="NextCursor">hand to the next <c>ListAsync()</c> call as cursor; null on the last page</param>
/// <param name="Total">sandboxes matching in all</param>
public sealed record PaginatedSandboxes(IReadOnlyList<Sandbox> Items, string? NextCursor, int Total);

/// <summary>Outcome of <c>ExecuteCommandAsync()</c> and <c>CodeRunAsync()</c>.</summary>
/// <param name="ExitCode">exit code of the command; 0 means success</param>
/// <param name="Result">combined output: stdout followed by stderr</param>
/// <param name="Artifacts">stdout and stderr apart, and charts</param>
public sealed record ExecuteResponse(int ExitCode, string Result, ExecutionArtifacts? Artifacts);

/// <summary>Output of a command, stream by stream.</summary>
/// <param name="Stdout">standard output</param>
/// <param name="Stderr">stderr kept apart from stdout</param>
/// <param name="Charts">charts matplotlib printed as artifacts; filled by <c>CodeRunAsync()</c> for Python</param>
public sealed record ExecutionArtifacts(string Stdout, string Stderr, IReadOnlyList<Chart>? Charts = null);

/// <summary>Which stream a chunk of output was written to.</summary>
public enum OutputStream
{
    /// <summary>standard output</summary>
    Stdout,
    /// <summary>standard error</summary>
    Stderr,
}

/// <summary>
/// mogenius: one event of <c>ExecuteCommandStreamAsync()</c>. Output arrives as bytes of the stream it was written to,
/// in order within each stream; the last event is always <see cref="ExecExitEvent"/>.
/// </summary>
public abstract record ExecEvent;

/// <summary>A chunk of output.</summary>
/// <param name="Stream">stdout or stderr</param>
/// <param name="Data">the bytes as written</param>
public sealed record ExecOutputEvent(OutputStream Stream, ReadOnlyMemory<byte> Data) : ExecEvent
{
    /// <summary>The chunk as UTF-8 text; a multi-byte character may be split between chunks.</summary>
    public string Text => Encoding.UTF8.GetString(Data.Span);
}

/// <summary>The command ended.</summary>
/// <param name="ExitCode">exit code of the command; 137 when it was killed, which a timeout does</param>
/// <param name="Truncated">a stream exceeded the cluster's output cap; what came after was dropped</param>
/// <param name="TimedOut">the command was stopped at its timeout</param>
public sealed record ExecExitEvent(int ExitCode, bool Truncated, bool TimedOut) : ExecEvent;

/// <summary>A chart <c>CodeRunAsync()</c> extracted from the output.</summary>
public sealed record Chart
{
    /// <summary>bar, pie, scatter, line, composite or unknown.</summary>
    [JsonPropertyName("type")] public string Type { get; init; } = "unknown";

    /// <summary>The figure's title.</summary>
    [JsonPropertyName("title")] public string? Title { get; init; }

    /// <summary>PNG, base64-encoded.</summary>
    [JsonPropertyName("png")] public string? Png { get; init; }

    /// <summary>Chart elements, when reported.</summary>
    [JsonPropertyName("elements")] public IReadOnlyList<JsonElement>? Elements { get; init; }
}

/// <summary>Options of <c>CodeRunAsync()</c>.</summary>
public sealed class CodeRunParams
{
    /// <summary>Arguments passed to the script (<c>sys.argv[1..]</c>, <c>process.argv[2..]</c>).</summary>
    public IReadOnlyList<string>? Argv { get; init; }

    /// <summary>Environment variables for this run.</summary>
    public IReadOnlyDictionary<string, string>? Env { get; init; }

    /// <summary>Overrides the sandbox's default language for this run.</summary>
    public CodeLanguage? Language { get; init; }

    /// <summary>mogenius: container to run in; the sandbox's own when absent.</summary>
    public string? Container { get; init; }
}

/// <summary>The sandbox as the platform returns it.</summary>
public sealed record SandboxInfo
{
    /// <summary>Name of the claim or sandbox object; what every route takes.</summary>
    public string Id { get; init; } = "";
    /// <summary>Name of the sandbox.</summary>
    public string Name { get; init; } = "";
    /// <summary>Kubernetes namespace.</summary>
    public string Namespace { get; init; } = "";
    /// <summary>Whether <see cref="Id"/> names a <c>SandboxClaim</c> (warm pool) or a <c>Sandbox</c> (own image).</summary>
    public string Kind { get; init; } = "";
    /// <summary>Current state.</summary>
    public SandboxState State { get; init; }
    /// <summary>Why the state is error or still starting.</summary>
    public string? StateReason { get; init; }
    /// <summary>Kubernetes labels.</summary>
    public IReadOnlyDictionary<string, string> Labels { get; init; } = new Dictionary<string, string>();
    /// <summary>Profile the sandbox was made from (snapshot).</summary>
    public string? Profile { get; init; }
    /// <summary>Image of the sandbox container.</summary>
    public string? Image { get; init; }
    /// <summary>The pod's name; null while creating.</summary>
    public string? PodName { get; init; }
    /// <summary>The Sandbox object that runs the pod; null while a claim is unbound.</summary>
    public string? SandboxName { get; init; }
    /// <summary>Container that commands, files and sessions address.</summary>
    public string ContainerName { get; init; } = "";
    /// <summary>The sandbox's address inside the cluster.</summary>
    [JsonPropertyName("serviceFQDN")] public string? ServiceFqdn { get; init; }
    /// <summary><c>Running</c> or <c>Suspended</c>.</summary>
    public string? OperatingMode { get; init; }
    /// <summary>Pod and storage go with the sandbox.</summary>
    public bool Ephemeral { get; init; }
    /// <summary>ISO timestamp the sandbox is shut down at; null without a deadline.</summary>
    public string? ExpiresAt { get; init; }
    /// <summary>ISO timestamp of creation.</summary>
    public string? CreatedAt { get; init; }
    /// <summary>Who created the sandbox.</summary>
    public string? CreatedBy { get; init; }
    /// <summary>Minutes without a toolbox call after which the sandbox stops; 0 never.</summary>
    public int? AutoStopInterval { get; init; }
    /// <summary>ISO timestamp of the last toolbox call the cluster saw; null before the first.</summary>
    public string? LastActivityAt { get; init; }
    /// <summary>The sandbox has a volume of its own: a stop keeps what lies on it.</summary>
    public bool? Persistent { get; init; }
}

// ---------------------------------------------------------------------------------------------------------------------
// sessions
// ---------------------------------------------------------------------------------------------------------------------

/// <summary>A session: one long-lived shell in the sandbox where state carries over between commands.</summary>
/// <param name="SessionId">the name given at creation</param>
/// <param name="Commands">commands run in the session so far, oldest first</param>
/// <param name="Container">mogenius: container the shell runs in</param>
/// <param name="CreatedAt">mogenius: ISO timestamp</param>
/// <param name="LastUsedAt">mogenius: ISO timestamp</param>
public sealed record SessionInfo(
    string SessionId,
    IReadOnlyList<SessionCommand> Commands,
    string Container,
    string CreatedAt,
    string LastUsedAt);

/// <summary>mogenius: options of <c>CreateSessionAsync()</c>.</summary>
public sealed class CreateSessionOptions
{
    /// <summary>Container the shell runs in; the sandbox's own when absent.</summary>
    public string? Container { get; init; }

    /// <summary>
    /// Exported into the session's shell, so every command of the session sees it — the place for API keys a
    /// long-running command needs, since a command's own text is listed with the session. Names must be shell
    /// identifiers.
    /// </summary>
    public IReadOnlyDictionary<string, string>? Env { get; init; }
}

/// <summary>A command run in a session.</summary>
/// <param name="Id">the cmdId the exec call returned</param>
/// <param name="Command">the command line</param>
/// <param name="ExitCode">null while the command is still running</param>
/// <param name="Truncated">the output went beyond the cluster's cap; the rest was dropped, the command ran on</param>
public sealed record SessionCommand(string Id, string Command, int? ExitCode = null, bool Truncated = false);

/// <summary>Request of <c>ExecuteSessionCommandAsync()</c>.</summary>
public sealed class SessionExecuteRequest
{
    /// <summary>The command line.</summary>
    public required string Command { get; init; }

    /// <summary>Return right away with the cmdId; fetch result and logs later.</summary>
    public bool RunAsync { get; init; }

    /// <summary>How long a synchronous call waits for the command (default 60 s); the command runs on past it.</summary>
    public TimeSpan? Timeout { get; init; }
}

/// <summary>Response of <c>ExecuteSessionCommandAsync()</c>.</summary>
/// <param name="CmdId">id of the command in the session</param>
/// <param name="ExitCode">set when the command ended within the wait</param>
/// <param name="Output">output so far, stdout and stderr in arrival order; null for a background run</param>
public sealed record SessionExecuteResponse(string CmdId, int? ExitCode = null, string? Output = null);

// ---------------------------------------------------------------------------------------------------------------------
// files
// ---------------------------------------------------------------------------------------------------------------------

/// <summary>One file or folder.</summary>
public sealed record SandboxFileInfo
{
    /// <summary>Base name.</summary>
    public string Name { get; init; } = "";
    /// <summary>mogenius only: absolute path inside the container.</summary>
    public string Path { get; init; } = "";
    /// <summary>A folder.</summary>
    public bool IsDir { get; init; }
    /// <summary>Size in bytes.</summary>
    public long Size { get; init; }
    /// <summary>ISO timestamp.</summary>
    public string ModTime { get; init; } = "";
    /// <summary>Octal, e.g. <c>0644</c>.</summary>
    public string Mode { get; init; } = "";
    /// <summary><c>ls -l</c> style, e.g. <c>-rw-r--r--</c>.</summary>
    public string Permissions { get; init; } = "";
    /// <summary>Owner.</summary>
    public string Owner { get; init; } = "";
    /// <summary>Group.</summary>
    public string Group { get; init; } = "";
    /// <summary>mogenius only: sniffed media type of a regular file.</summary>
    public string? MimeType { get; init; }
}

/// <summary>One entry of <c>UploadFilesAsync()</c>; the source is bytes, not a local file path.</summary>
/// <param name="Source">the file's content</param>
/// <param name="Destination">destination path inside the container</param>
public sealed record FileUpload(ReadOnlyMemory<byte> Source, string Destination)
{
    /// <summary>An upload of UTF-8 text.</summary>
    public static FileUpload FromText(string text, string destination) =>
        new(Encoding.UTF8.GetBytes(text), destination);
}

/// <summary>One file's outcome of an upload.</summary>
public sealed record UploadResult(string Path, bool Success, string? Error);

/// <summary>mogenius only: how far an upload got.</summary>
/// <param name="BytesSent">bytes read from the source so far</param>
public readonly record struct UploadProgress(long BytesSent);

/// <summary>mogenius only: how far a streamed download got.</summary>
/// <param name="BytesReceived">bytes read so far</param>
/// <param name="TotalBytes">size of the file; 0 when unknown</param>
public readonly record struct DownloadProgress(long BytesReceived, long TotalBytes);

/// <summary>Result of <c>SearchFilesAsync()</c>.</summary>
/// <param name="Files">matching paths</param>
/// <param name="Truncated">mogenius only: more matched than were returned</param>
public sealed record SearchFilesResponse(IReadOnlyList<string> Files, bool Truncated);

/// <summary>One matching line of <c>FindFilesAsync()</c>.</summary>
public sealed record FileMatch(string File, int Line, string Content);

/// <summary>One file's outcome of <c>ReplaceInFilesAsync()</c>.</summary>
public sealed record ReplaceResult(string File, bool Success, string? Error);

/// <summary>One file's outcome of <c>DownloadFilesAsync()</c>.</summary>
public sealed record DownloadFileResult(string Path, byte[]? Data, string? Error);

/// <summary>Parameters of <c>SetFilePermissionsAsync()</c>; what is not given stays.</summary>
/// <param name="Mode">octal (<c>755</c>) or symbolic (<c>u+x</c>)</param>
/// <param name="Owner">new owner</param>
/// <param name="Group">new group</param>
public sealed record SetFilePermissionsParams(string? Mode = null, string? Owner = null, string? Group = null);

/// <summary>A SandboxWarmPool (mogenius): the pre-started sandboxes <c>CreateAsync()</c> claims from.</summary>
/// <param name="Name">pool name</param>
/// <param name="Namespace">namespace of the pool</param>
/// <param name="Template">the SandboxTemplate the pool's sandboxes are stamped from</param>
/// <param name="Ready">how many sandboxes wait to be claimed</param>
/// <param name="Size">how many sandboxes the pool keeps warm</param>
public sealed record WarmPool(string Name, string Namespace, string Template, int Ready, int Size);
