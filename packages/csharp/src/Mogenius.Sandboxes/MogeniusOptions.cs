using System.Net.WebSockets;

namespace Mogenius.Sandboxes;

/// <summary>How the SDK reaches the platform. Every field falls back to an environment variable.</summary>
public sealed class MogeniusOptions
{
    /// <summary>Personal access token or API key (<c>mo_pat:…</c>). Env: <c>MOGENIUS_API_KEY</c>.</summary>
    public string? ApiKey { get; set; }

    /// <summary>Platform API base URL. Env: <c>MOGENIUS_API_URL</c>. Default: https://platform-api.mogenius.com</summary>
    public string? ApiUrl { get; set; }

    /// <summary>
    /// Organization the key acts in. Env: <c>MOGENIUS_ORGANIZATION_ID</c>. Optional for keys with one organization
    /// scope.
    /// </summary>
    public string? OrganizationId { get; set; }

    /// <summary>Cluster the sandboxes run on. Env: <c>MOGENIUS_CLUSTER_ID</c>. Optional for keys with one cluster scope.</summary>
    public string? ClusterId { get; set; }

    /// <summary>
    /// Kubernetes namespace the sandbox chart puts sandboxes in (<c>sandboxes.namespace.name</c>).
    /// Env: <c>MOGENIUS_SANDBOX_NAMESPACE</c>. Default <c>agent-sandbox</c>. <see cref="Target"/> maps here.
    /// </summary>
    public string? Namespace { get; set; }

    /// <summary>Alias of <see cref="Namespace"/>, for programs that pass <c>target</c>.</summary>
    public string? Target { get; set; }

    /// <summary>
    /// mogenius: the warm pool <c>CreateAsync()</c> claims from when the params name none.
    /// Env: <c>MOGENIUS_SANDBOX_PROFILE</c>. Default <c>default</c>.
    /// </summary>
    public string? Profile { get; set; }

    /// <summary>Workspace to act through when the key has no cluster role. Header <c>workspace-name</c>.</summary>
    public string? WorkspaceName { get; set; }

    /// <summary>
    /// mogenius: the platform's stream gateway, for streams and tunnels.
    /// Env: <c>MOGENIUS_STREAM_URL</c>. Default: wss://k8s-cmd-stream.mogenius.com
    /// </summary>
    public string? StreamUrl { get; set; }

    /// <summary>
    /// mogenius: how uploads reach the sandbox when the call names none: <see cref="FileTransport.Http"/> (the
    /// platform API) or <see cref="FileTransport.Sftp"/> (SFTP through the stream gateway, past any web application
    /// firewall in front of the API). Env: <c>MOGENIUS_FILE_TRANSPORT</c>. Default <c>http</c>.
    /// </summary>
    public FileTransport? FileTransport { get; set; }

    /// <summary>Replaces the SDK's own <see cref="System.Net.Http.HttpClient"/>; for tests and custom agents.</summary>
    public HttpClient? HttpClient { get; set; }

    /// <summary>
    /// Opens a WebSocket to the given URL; replaces <see cref="ClientWebSocket"/>; for tests and custom agents.
    /// </summary>
    public Func<Uri, CancellationToken, Task<WebSocket>>? WebSocketFactory { get; set; }
}

/// <summary>Defaults and resolution of <see cref="MogeniusOptions"/>.</summary>
public static class MogeniusDefaults
{
    /// <summary>Default platform API.</summary>
    public const string ApiUrl = "https://platform-api.mogenius.com";

    /// <summary>Default stream gateway.</summary>
    public const string StreamUrl = "wss://k8s-cmd-stream.mogenius.com";

    /// <summary>Default sandbox namespace.</summary>
    public const string Namespace = "agent-sandbox";

    /// <summary>Default profile (warm pool).</summary>
    public const string Profile = "default";

    /// <summary>Default when a command names no timeout.</summary>
    public static readonly TimeSpan CommandTimeout = TimeSpan.FromSeconds(10);
}

/// <summary>Everything a sandbox needs from the options, resolved.</summary>
internal sealed record ResolvedOptions(
    string ApiKey,
    string ApiUrl,
    string StreamUrl,
    string? OrganizationId,
    string? ClusterId,
    string Namespace,
    string Profile,
    string? WorkspaceName,
    FileTransport FileTransport,
    HttpClient? HttpClient,
    Func<Uri, CancellationToken, Task<WebSocket>>? WebSocketFactory)
{
    /// <summary>
    /// Explicit options win, environment variables fill the gaps. Only the key is required: an API key with a single
    /// organization and cluster scope needs no ids, the platform picks them from the key.
    /// </summary>
    public static ResolvedOptions Resolve(MogeniusOptions? options)
    {
        options ??= new MogeniusOptions();
        var apiKey = options.ApiKey ?? Env("MOGENIUS_API_KEY");
        if (string.IsNullOrEmpty(apiKey))
        {
            throw new MogeniusException(
                "No API key: set `ApiKey` in MogeniusOptions or set MOGENIUS_API_KEY. Create one under Organization → API keys.");
        }

        FileTransport fileTransport;
        if (options.FileTransport is { } explicitTransport)
        {
            fileTransport = explicitTransport;
        }
        else
        {
            var text = (Env("MOGENIUS_FILE_TRANSPORT") ?? "http").ToLowerInvariant();
            fileTransport = text switch
            {
                "http" => Sandboxes.FileTransport.Http,
                "sftp" => Sandboxes.FileTransport.Sftp,
                _ => throw new MogeniusValidationException(
                    $"fileTransport (MOGENIUS_FILE_TRANSPORT) is \"{text}\"; it is http or sftp."),
            };
        }

        return new ResolvedOptions(
            ApiKey: apiKey,
            ApiUrl: (options.ApiUrl ?? Env("MOGENIUS_API_URL") ?? MogeniusDefaults.ApiUrl).TrimEnd('/'),
            StreamUrl: (options.StreamUrl ?? Env("MOGENIUS_STREAM_URL") ?? MogeniusDefaults.StreamUrl).TrimEnd('/'),
            OrganizationId: options.OrganizationId ?? Env("MOGENIUS_ORGANIZATION_ID"),
            ClusterId: options.ClusterId ?? Env("MOGENIUS_CLUSTER_ID"),
            Namespace: options.Namespace ?? options.Target ?? Env("MOGENIUS_SANDBOX_NAMESPACE") ??
                       MogeniusDefaults.Namespace,
            Profile: options.Profile ?? Env("MOGENIUS_SANDBOX_PROFILE") ?? MogeniusDefaults.Profile,
            WorkspaceName: options.WorkspaceName ?? Env("MOGENIUS_WORKSPACE_NAME"),
            FileTransport: fileTransport,
            HttpClient: options.HttpClient,
            WebSocketFactory: options.WebSocketFactory);
    }

    private static string? Env(string name)
    {
        var value = Environment.GetEnvironmentVariable(name)?.Trim();
        return string.IsNullOrEmpty(value) ? null : value;
    }
}
