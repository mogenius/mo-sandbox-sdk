using System.Runtime.CompilerServices;
using System.Text;
using System.Text.Json;

namespace Mogenius.Sandboxes;

/// <summary>Pod and container a sandbox's pod routes address.</summary>
internal readonly record struct PodRef(string PodName, string ContainerName);

/// <summary>
/// Processes of a sandbox: run a command or a snippet of code and get exit code and output back. Everything goes
/// through the platform API to the operator, which executes inside the pod without a TTY — no agent in the image, no
/// open port.
/// </summary>
public sealed class ProcessService
{
    // Frames of an exec stream, as the operator writes them: output behind a one-byte stream tag, control messages
    // as text.
    private const byte StreamTagStderr = 1;
    private const string StreamExitPrefix = "EXIT:";
    private const string StreamErrorPrefix = "ERROR:";
    private const string StreamTruncated = "TRUNCATED";
    private const string StreamTimeout = "TIMEOUT";
    // the gateway asks for the command once the connection is authorized; it does not travel in the URL
    private const string StreamRequestPrompt = "SEND_EXEC_REQUEST";

    /// <summary>Start of the line the Python bootstrap prints for each matplotlib figure; the rest is the chart JSON.</summary>
    private const string ChartMarker = "__mo_chart__:";

    private readonly ApiClient _api;
    private readonly string _namespace;
    private readonly CodeLanguage _defaultLanguage;
    private readonly Func<CancellationToken, Task<PodRef>> _pod;

    internal ProcessService(ApiClient api, string ns, CodeLanguage defaultLanguage,
        Func<CancellationToken, Task<PodRef>> pod)
    {
        _api = api;
        _namespace = ns;
        _defaultLanguage = defaultLanguage;
        _pod = pod;
    }

    /// <summary>
    /// Runs a shell command line. Pipes, <c>&amp;&amp;</c> and quoting behave as in the image's shell. Stateless:
    /// <c>cd</c> and <c>export</c> do not carry over to the next call — pass <paramref name="cwd"/> and
    /// <paramref name="env"/> instead.
    /// </summary>
    /// <param name="command">the command line</param>
    /// <param name="cwd">working directory inside the container; the container's own when null</param>
    /// <param name="env">environment variables for this run; keys must be shell identifiers</param>
    /// <param name="timeout">until the command is stopped (default 10 s); the platform caps it</param>
    /// <param name="container">mogenius: container to run in; the sandbox's own when null</param>
    /// <param name="cancellationToken">cancels the request</param>
    public async Task<ExecuteResponse> ExecuteCommandAsync(string command, string? cwd = null,
        IReadOnlyDictionary<string, string>? env = null, TimeSpan? timeout = null, string? container = null,
        CancellationToken cancellationToken = default)
    {
        var pod = await _pod(cancellationToken).ConfigureAwait(false);
        // commands are a pod feature: the platform serves them on its pod route
        var body = await _api.PostAsync<ExecuteCommandResponseBody>(
            $"/resource/exec/{Uri.EscapeDataString(_namespace)}/{Uri.EscapeDataString(pod.PodName)}",
            ExecRequest(command, cwd, env, timeout ?? MogeniusDefaults.CommandTimeout,
                string.IsNullOrEmpty(container) ? pod.ContainerName : container),
            ct: cancellationToken).ConfigureAwait(false);
        return new ExecuteResponse(body.ExitCode, body.Result ?? "",
            new ExecutionArtifacts(body.Artifacts?.Stdout ?? "", body.Artifacts?.Stderr ?? ""));
    }

    /// <summary>
    /// mogenius: runs a command like <see cref="ExecuteCommandAsync"/>, but delivers its output while it runs — for
    /// builds, tests and servers. Yields stdout and stderr chunks as they arrive and ends with one
    /// <see cref="ExecExitEvent"/> carrying the exit code. Leaving the loop early closes the stream, which stops the
    /// command in the container within seconds. The command is stateless and gets no stdin.
    /// Needs <c>OrganizationId</c>, <c>ClusterId</c> and the stream gateway.
    /// </summary>
    /// <param name="command">the command line</param>
    /// <param name="cwd">working directory inside the container; the container's own when null</param>
    /// <param name="env">environment variables for this run; keys must be shell identifiers</param>
    /// <param name="timeout">until the command is stopped (default 10 s); the platform caps it</param>
    /// <param name="container">container to run in; the sandbox's own when null</param>
    /// <param name="cancellationToken">stops reading and closes the stream</param>
    public async IAsyncEnumerable<ExecEvent> ExecuteCommandStreamAsync(string command, string? cwd = null,
        IReadOnlyDictionary<string, string>? env = null, TimeSpan? timeout = null, string? container = null,
        [EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        var pod = await _pod(cancellationToken).ConfigureAwait(false);
        var socket = await _api.OpenStreamAsync(new Query
        {
            ["type"] = "CLUSTER__POD_EXEC",
            ["cmd"] = "exec",
            ["namespace"] = _namespace,
            ["podName"] = pod.PodName,
            ["container"] = string.IsNullOrEmpty(container) ? pod.ContainerName : container,
            ["binary"] = "1",
        }, cancellationToken).ConfigureAwait(false);
        // the same body as the exec route, sent once the gateway asks for it
        var request = JsonSerializer.Serialize(
            ExecRequest(command, cwd, env, timeout ?? MogeniusDefaults.CommandTimeout, container), ApiClient.Json);
        await foreach (var item in StreamEventsAsync(socket, request, cancellationToken).ConfigureAwait(false))
        {
            yield return item;
        }
    }

    /// <summary>
    /// Reads one gateway stream into events: output frames by their stream tag, control frames into the final exit
    /// event or an error. <paramref name="request"/>, when given, is sent as the gateway asks for it (an exec
    /// stream); a log follow has nothing to send. Leaving the enumeration closes the socket.
    /// </summary>
    private async IAsyncEnumerable<ExecEvent> StreamEventsAsync(GatewaySocket socket, string? request,
        [EnumeratorCancellation] CancellationToken ct)
    {
        await using var _ = socket.ConfigureAwait(false);
        int? exitCode = null;
        var truncated = false;
        var timedOut = false;
        while (true)
        {
            var received = await socket.ReceiveAsync(ct).ConfigureAwait(false);
            if (received is not { } frame)
            {
                if (exitCode is { } code)
                {
                    yield return new ExecExitEvent(code, truncated, timedOut);
                    yield break;
                }

                throw StreamErrors.FromClose(socket.CloseCode, socket.CloseReason, _api.StreamUrl);
            }

            if (!frame.IsText)
            {
                if (frame.Data.Length > 1)
                {
                    yield return new ExecOutputEvent(
                        frame.Data[0] == StreamTagStderr ? OutputStream.Stderr : OutputStream.Stdout,
                        frame.Data.AsMemory(1));
                }

                continue;
            }

            var text = frame.Text;
            if (text == StreamRequestPrompt)
            {
                if (request is not null)
                {
                    await socket.SendTextAsync(request, ct).ConfigureAwait(false);
                }
            }
            else if (text.StartsWith(StreamExitPrefix, StringComparison.Ordinal))
            {
                exitCode = int.TryParse(text.AsSpan(StreamExitPrefix.Length), out var parsed) ? parsed : -1;
            }
            else if (text == StreamTruncated)
            {
                truncated = true;
            }
            else if (text == StreamTimeout)
            {
                timedOut = true;
            }
            else if (text.StartsWith(StreamErrorPrefix, StringComparison.Ordinal))
            {
                throw new MogeniusException(text[StreamErrorPrefix.Length..], source: ErrorSource.Operator);
            }
            // anything else (PEER_IS_READY, pings) is the gateway talking to itself
        }
    }

    /// <summary>
    /// Runs a snippet of code with the sandbox's default language (or <see cref="CodeRunParams.Language"/>): Python
    /// through <c>python3</c>, TypeScript through <c>tsx</c>, JavaScript through <c>node</c>. The code travels
    /// base64-encoded, so quotes and newlines need no escaping. Figures a Python run draws with matplotlib come back
    /// as PNG charts in <see cref="ExecutionArtifacts.Charts"/> — on <c>plt.show()</c> and for what is still open at
    /// the end.
    /// </summary>
    public async Task<ExecuteResponse> CodeRunAsync(string code, CodeRunParams? parameters = null,
        TimeSpan? timeout = null, CancellationToken cancellationToken = default)
    {
        var language = parameters?.Language ?? _defaultLanguage;
        var response = await ExecuteCommandAsync(BuildCodeRunCommand(code, language, parameters?.Argv ?? []),
            null, parameters?.Env, timeout, parameters?.Container, cancellationToken).ConfigureAwait(false);
        if (language != CodeLanguage.Python)
        {
            return response;
        }

        var stderr = response.Artifacts?.Stderr ?? "";
        var (text, charts) = ExtractCharts(response.Artifacts?.Stdout ?? "");
        return new ExecuteResponse(response.ExitCode, text + stderr,
            new ExecutionArtifacts(text, stderr, charts.Count > 0 ? charts : null));
    }

    // -----------------------------------------------------------------------------------------------------------------
    // sessions
    // -----------------------------------------------------------------------------------------------------------------

    /// <summary>
    /// Opens a session: a shell of its own in the sandbox where state carries over from command to command
    /// (<c>cd</c>, <c>export</c>, a virtualenv). Commands run in it one at a time. The session lives until
    /// <see cref="DeleteSessionAsync"/>, until it has idled for the cluster's timeout (30 minutes by default), or
    /// until the sandbox stops.
    /// </summary>
    /// <param name="sessionId">a name of your choice, unique within the sandbox</param>
    /// <param name="options">mogenius: the container the shell runs in and env exported into its shell</param>
    /// <param name="cancellationToken">cancels the request</param>
    public async Task<SessionInfo> CreateSessionAsync(string sessionId, CreateSessionOptions? options = null,
        CancellationToken cancellationToken = default)
    {
        var path = await SessionPathAsync(null, cancellationToken).ConfigureAwait(false);
        // the pod route would take the pod's first container; a sandbox means its own
        var target = string.IsNullOrEmpty(options?.Container)
            ? (await _pod(cancellationToken).ConfigureAwait(false)).ContainerName
            : options.Container;
        var body = new Dictionary<string, object?> { ["sessionId"] = sessionId };
        if (!string.IsNullOrEmpty(target))
        {
            body["container"] = target;
        }

        if (options?.Env is { Count: > 0 } env)
        {
            body["env"] = env;
        }

        return ToSessionInfo(await _api.PostAsync<SessionInfoBody>(path, body, ct: cancellationToken)
            .ConfigureAwait(false));
    }

    /// <summary>Opens a session in that container.</summary>
    public Task<SessionInfo> CreateSessionAsync(string sessionId, string container,
        CancellationToken cancellationToken = default) =>
        CreateSessionAsync(sessionId, new CreateSessionOptions { Container = container }, cancellationToken);

    /// <summary>The session with its commands so far.</summary>
    public async Task<SessionInfo> GetSessionAsync(string sessionId, CancellationToken cancellationToken = default) =>
        ToSessionInfo(await _api.GetAsync<SessionInfoBody>(
            await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false),
            ct: cancellationToken).ConfigureAwait(false));

    /// <summary>Sessions of this sandbox that your key opened and that are still running.</summary>
    public async Task<IReadOnlyList<SessionInfo>> ListSessionsAsync(CancellationToken cancellationToken = default)
    {
        var body = await _api.GetAsync<List<SessionInfoBody>?>(
            await SessionPathAsync(null, cancellationToken).ConfigureAwait(false),
            ct: cancellationToken).ConfigureAwait(false);
        return (body ?? []).Select(ToSessionInfo).ToList();
    }

    /// <summary>Ends the session's shell. A command still running in it is stopped with it.</summary>
    public async Task DeleteSessionAsync(string sessionId, CancellationToken cancellationToken = default) =>
        await _api.DeleteAsync<JsonElement?>(await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false),
            ct: cancellationToken).ConfigureAwait(false);

    /// <summary>
    /// Runs a command in the session. Synchronous by default: waits up to <see cref="SessionExecuteRequest.Timeout"/>
    /// (60 s) and answers with the output and exit code. A command still running when the wait is over is not
    /// stopped — the answer then has no exit code and the result is fetched later, like after
    /// <see cref="SessionExecuteRequest.RunAsync"/>, which returns right away with the cmdId. An <c>exit</c> in the
    /// command ends the shell and with it the session.
    /// </summary>
    public async Task<SessionExecuteResponse> ExecuteSessionCommandAsync(string sessionId,
        SessionExecuteRequest request, CancellationToken cancellationToken = default)
    {
        var body = new Dictionary<string, object?> { ["command"] = request.Command };
        if (request.RunAsync)
        {
            body["runAsync"] = true;
        }

        if (request.Timeout is { TotalSeconds: > 0 } timeout)
        {
            body["timeout"] = (int)Math.Ceiling(timeout.TotalSeconds);
        }

        var answer = await _api.PostAsync<SessionExecuteResponseBody>(
            $"{await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false)}/exec", body,
            ct: cancellationToken).ConfigureAwait(false);
        return new SessionExecuteResponse(answer.CmdId, answer.ExitCode, answer.Output);
    }

    /// <summary>A command of the session, with its exit code once it has ended.</summary>
    public async Task<SessionCommand> GetSessionCommandAsync(string sessionId, string cmdId,
        CancellationToken cancellationToken = default) =>
        ToSessionCommand(await _api.GetAsync<SessionCommandBody>(
            $"{await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false)}/command/{Uri.EscapeDataString(cmdId)}",
            ct: cancellationToken).ConfigureAwait(false));

    /// <summary>What a session command has printed so far, stdout and stderr in arrival order.</summary>
    public async Task<string> GetSessionCommandLogsAsync(string sessionId, string cmdId,
        CancellationToken cancellationToken = default)
    {
        var body = await _api.GetAsync<LogsBody>(
            $"{await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false)}/command/{Uri.EscapeDataString(cmdId)}/logs",
            ct: cancellationToken).ConfigureAwait(false);
        return body?.Output ?? "";
    }

    /// <summary>
    /// Follows a session command live over the stream gateway — what was printed first, then every new chunk as it
    /// comes — and completes when the command has ended.
    /// </summary>
    public async Task GetSessionCommandLogsAsync(string sessionId, string cmdId, Action<string> onLogs,
        CancellationToken cancellationToken = default)
    {
        var pod = await _pod(cancellationToken).ConfigureAwait(false);
        var socket = await _api.OpenStreamAsync(new Query
        {
            ["type"] = "CLUSTER__POD_SESSION_LOG",
            ["cmd"] = "session-log",
            ["namespace"] = _namespace,
            ["podName"] = pod.PodName,
            ["sessionId"] = sessionId,
            ["cmdId"] = cmdId,
            ["binary"] = "1",
        }, cancellationToken).ConfigureAwait(false);
        var decoder = Encoding.UTF8.GetDecoder();
        await foreach (var item in StreamEventsAsync(socket, null, cancellationToken).ConfigureAwait(false))
        {
            if (item is ExecOutputEvent output)
            {
                var chars = new char[decoder.GetCharCount(output.Data.Span, false)];
                var count = decoder.GetChars(output.Data.Span, chars, false);
                if (count > 0)
                {
                    onLogs(new string(chars, 0, count));
                }
            }
        }

        var rest = new char[decoder.GetCharCount([], true)];
        var restCount = decoder.GetChars([], rest, true);
        if (restCount > 0)
        {
            onLogs(new string(rest, 0, restCount));
        }
    }

    /// <summary>
    /// Writes to the standard input of the command running in the session — what <c>read</c> or an interactive
    /// program is waiting for. Written verbatim: end a line with <c>\n</c>.
    /// </summary>
    public async Task SendSessionCommandInputAsync(string sessionId, string cmdId, string data,
        CancellationToken cancellationToken = default) =>
        await _api.PostAsync<JsonElement?>(
            $"{await SessionPathAsync(sessionId, cancellationToken).ConfigureAwait(false)}/command/{Uri.EscapeDataString(cmdId)}/input",
            new { data }, ct: cancellationToken).ConfigureAwait(false);

    /// <summary>Sessions belong to the pod, not to the sandbox object: the platform serves them on its pod routes.</summary>
    private async Task<string> SessionPathAsync(string? sessionId, CancellationToken ct)
    {
        var pod = await _pod(ct).ConfigureAwait(false);
        var basePath = $"/resource/session/{Uri.EscapeDataString(_namespace)}/{Uri.EscapeDataString(pod.PodName)}";
        return sessionId is null ? basePath : $"{basePath}/{Uri.EscapeDataString(sessionId)}";
    }

    // -----------------------------------------------------------------------------------------------------------------
    // helpers
    // -----------------------------------------------------------------------------------------------------------------

    /// <summary>The body of the pod exec route, and the first frame of an exec stream.</summary>
    private static Dictionary<string, object?> ExecRequest(string command, string? cwd,
        IReadOnlyDictionary<string, string>? env, TimeSpan timeout, string? container)
    {
        var body = new Dictionary<string, object?> { ["command"] = command };
        if (!string.IsNullOrEmpty(cwd))
        {
            body["cwd"] = cwd;
        }

        if (env is { Count: > 0 })
        {
            body["env"] = env;
        }

        if (timeout > TimeSpan.Zero)
        {
            body["timeout"] = (int)Math.Ceiling(timeout.TotalSeconds);
        }

        if (!string.IsNullOrEmpty(container))
        {
            body["container"] = container;
        }

        return body;
    }

    private static SessionCommand ToSessionCommand(SessionCommandBody body) =>
        new(body.Id, body.Command, body.ExitCode, body.Truncated ?? false);

    private static SessionInfo ToSessionInfo(SessionInfoBody body) =>
        new(body.SessionId, (body.Commands ?? []).Select(ToSessionCommand).ToList(), body.Container ?? "",
            body.CreatedAt ?? "", body.LastUsedAt ?? "");

    /// <summary>Single-quotes a value for a POSIX shell.</summary>
    internal static string ShellQuote(string value) => $"'{value.Replace("'", "'\\''", StringComparison.Ordinal)}'";

    /// <summary>
    /// The command line that writes the snippet to a temp file, runs it with the language's interpreter and removes
    /// the file again, preserving the interpreter's exit code. Everything the user wrote is inside the base64
    /// literal; argv entries are quoted one by one.
    /// </summary>
    internal static string BuildCodeRunCommand(string code, CodeLanguage language, IReadOnlyList<string> argv)
    {
        var encoded = Convert.ToBase64String(Encoding.UTF8.GetBytes(code));
        var args = string.Join(' ', argv.Select(ShellQuote));
        var (extension, interpreter) = Interpreter(language);
        var file = $"/tmp/mo_code_$$.{extension}";
        return $"__mo_f={file}; printf %s '{encoded}' | base64 -d > \"$__mo_f\" && " +
               $"{interpreter} \"$__mo_f\"{(args.Length > 0 ? $" {args}" : "")}; __mo_rc=$?; rm -f \"$__mo_f\"; exit $__mo_rc";
    }

    private static (string Extension, string Interpreter) Interpreter(CodeLanguage language) => language switch
    {
        // the bootstrap runs the snippet and turns matplotlib figures into chart lines; it travels base64-encoded too
        CodeLanguage.Python => ("py",
            $"python3 -c \"$(printf %s '{Convert.ToBase64String(Encoding.UTF8.GetBytes(PythonBootstrap.Source))}' | base64 -d)\""),
        CodeLanguage.TypeScript => ("ts", "tsx"),
        CodeLanguage.JavaScript => ("js", "node"),
        _ => throw new MogeniusValidationException($"Unknown code language {language}."),
    };

    /// <summary>Splits the chart lines out of stdout; the rest is the program's own output.</summary>
    internal static (string Text, List<Chart> Charts) ExtractCharts(string stdout)
    {
        var charts = new List<Chart>();
        var kept = new StringBuilder();
        var start = 0;
        // line by line, endings kept: a chart's JSON is the whole rest of its line
        while (start < stdout.Length)
        {
            var newline = stdout.IndexOf('\n', start);
            var end = newline < 0 ? stdout.Length : newline + 1;
            var line = stdout[start..end];
            start = end;
            if (line.StartsWith(ChartMarker, StringComparison.Ordinal))
            {
                try
                {
                    var chart = JsonSerializer.Deserialize<Chart>(line[ChartMarker.Length..], ApiClient.Json);
                    if (chart is not null)
                    {
                        charts.Add(chart);
                        continue;
                    }
                }
                catch (JsonException)
                {
                    // not a chart after all: the line stays
                }
            }

            kept.Append(line);
        }

        return (kept.ToString(), charts);
    }

    // what the platform answers

    private sealed record ExecuteCommandResponseBody(
        int ExitCode,
        string? Result,
        ArtifactsBody? Artifacts,
        bool Truncated,
        string? Container,
        long DurationMs);

    private sealed record ArtifactsBody(string? Stdout, string? Stderr);

    private sealed record SessionInfoBody(
        string SessionId,
        string? Container,
        string? CreatedAt,
        string? LastUsedAt,
        List<SessionCommandBody>? Commands);

    private sealed record SessionCommandBody(string Id, string Command, int? ExitCode, bool? Truncated);

    private sealed record SessionExecuteResponseBody(string CmdId, int? ExitCode, string? Output);

    private sealed record LogsBody(string? Output);
}

/// <summary>What <c>CodeRunAsync()</c> starts a Python snippet with; the Go SDK's code_run.py, embedded.</summary>
internal static class PythonBootstrap
{
    public static readonly string Source = Load();

    private static string Load()
    {
        using var stream = typeof(PythonBootstrap).Assembly.GetManifestResourceStream("Mogenius.Sandboxes.code_run.py")
                           ?? throw new InvalidOperationException("code_run.py is not embedded.");
        using var reader = new StreamReader(stream, Encoding.UTF8);
        return reader.ReadToEnd();
    }
}
