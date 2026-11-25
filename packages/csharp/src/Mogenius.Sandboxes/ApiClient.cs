using System.Net.Http.Headers;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization;

namespace Mogenius.Sandboxes;

/// <summary>
/// The HTTP layer under the SDK: one place for the platform's headers, JSON handling and the error shape. Routes are
/// the platform's <c>/sandbox/...</c> and <c>/resource/...</c> endpoints.
/// </summary>
internal sealed class ApiClient : IDisposable
{
    private const string UserAgent = "Mogenius.Sandboxes";

    internal static readonly JsonSerializerOptions Json = new(JsonSerializerDefaults.Web)
    {
        DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull,
    };

    private readonly ResolvedOptions _options;
    private readonly HttpClient _http;
    private readonly bool _ownsHttp;

    public ApiClient(ResolvedOptions options)
    {
        _options = options;
        _ownsHttp = options.HttpClient is null;
        _http = options.HttpClient ?? new HttpClient(new SocketsHttpHandler
        {
            PooledConnectionLifetime = TimeSpan.FromMinutes(5),
        })
        {
            Timeout = Timeout.InfiniteTimeSpan,
        };
    }

    public string Namespace => _options.Namespace;
    public string StreamUrl => _options.StreamUrl;
    public FileTransport FileTransport => _options.FileTransport;

    public Task<T> GetAsync<T>(string path, Query? query = null, CancellationToken ct = default) =>
        RequestAsync<T>(HttpMethod.Get, path, query, null, ct);

    public Task<T> PostAsync<T>(string path, object? body = null, Query? query = null, CancellationToken ct = default) =>
        RequestAsync<T>(HttpMethod.Post, path, query, body is null ? null : JsonContent(body), ct);

    /// <summary>POST with a prepared body: a multipart form, possibly streamed.</summary>
    public Task<T> PostContentAsync<T>(string path, HttpContent content, Query? query = null,
        CancellationToken ct = default) =>
        RequestAsync<T>(HttpMethod.Post, path, query, content, ct);

    public Task<T> PatchAsync<T>(string path, object? body = null, CancellationToken ct = default) =>
        RequestAsync<T>(HttpMethod.Patch, path, null, body is null ? null : JsonContent(body), ct);

    public Task<T> DeleteAsync<T>(string path, Query? query = null, CancellationToken ct = default) =>
        RequestAsync<T>(HttpMethod.Delete, path, query, null, ct);

    /// <summary>
    /// GET of an absolute URL the platform handed out (a download link). No auth headers: the token in the URL is
    /// the credential, the way a browser opens the same link. With <paramref name="offset"/> it asks to continue at
    /// that byte, but only if the file is still the version <paramref name="ifRange"/> names. The caller owns the
    /// response.
    /// </summary>
    public async Task<HttpResponseMessage> FetchDownloadAsync(string url, long? offset = null, string? ifRange = null,
        CancellationToken ct = default)
    {
        using var request = new HttpRequestMessage(HttpMethod.Get, url);
        request.Headers.TryAddWithoutValidation("accept", "*/*");
        request.Headers.TryAddWithoutValidation("user-agent", UserAgent);
        if (offset is { } from)
        {
            request.Headers.Range = new RangeHeaderValue(from, null);
            if (ifRange is not null)
            {
                request.Headers.TryAddWithoutValidation("if-range", ifRange);
            }
        }

        HttpResponseMessage response;
        try
        {
            response = await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, ct)
                .ConfigureAwait(false);
        }
        catch (HttpRequestException err)
        {
            throw new MogeniusException($"GET {url} failed: {err.Message}", source: ErrorSource.Sdk,
                innerException: err);
        }

        if (!response.IsSuccessStatusCode)
        {
            using (response)
            {
                var text = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
                throw MogeniusErrors.FromResponse((int)response.StatusCode, text,
                    $"GET download link → HTTP {(int)response.StatusCode}");
            }
        }

        return response;
    }

    /// <summary>
    /// Opens a socket on the platform's stream gateway. The gateway reads the query string like headers, so the key
    /// and the ids travel there next to <paramref name="query"/>. Unlike the HTTP routes, the gateway does not infer
    /// organization and cluster from the key, so both have to be configured.
    /// </summary>
    public async Task<GatewaySocket> OpenStreamAsync(Query query, CancellationToken ct = default)
    {
        if (string.IsNullOrEmpty(_options.OrganizationId) || string.IsNullOrEmpty(_options.ClusterId))
        {
            throw new MogeniusException(
                "Streams need `OrganizationId` and `ClusterId` (MOGENIUS_ORGANIZATION_ID, MOGENIUS_CLUSTER_ID): the stream gateway does not take them from the key.");
        }

        var all = new Query
        {
            ["authorization"] = $"bearer {_options.ApiKey}",
            ["organizationId"] = _options.OrganizationId,
            ["clusterId"] = _options.ClusterId,
            ["workspaceName"] = _options.WorkspaceName,
        };
        foreach (var (key, value) in query)
        {
            all[key] = value;
        }

        var uri = new Uri(AppendQuery($"{_options.StreamUrl}/xterm-stream", all));
        WebSocket socket;
        try
        {
            if (_options.WebSocketFactory is { } factory)
            {
                socket = await factory(uri, ct).ConfigureAwait(false);
            }
            else
            {
                var client = new ClientWebSocket();
                client.Options.SetRequestHeader("user-agent", UserAgent);
                try
                {
                    await client.ConnectAsync(uri, ct).ConfigureAwait(false);
                }
                catch
                {
                    client.Dispose();
                    throw;
                }

                socket = client;
            }
        }
        catch (Exception err) when (err is WebSocketException or HttpRequestException)
        {
            // the handshake failed: what a browser reports as close code 1006
            throw StreamErrors.FromClose(1006, null, _options.StreamUrl);
        }

        return new GatewaySocket(socket);
    }

    private static StringContent JsonContent(object body) =>
        new(JsonSerializer.Serialize(body, Json), Encoding.UTF8, "application/json");

    private async Task<T> RequestAsync<T>(HttpMethod method, string path, Query? query, HttpContent? content,
        CancellationToken ct)
    {
        using var response = await SendAsync(method, path, query, content, ct).ConfigureAwait(false);
        var text = await response.Content.ReadAsStringAsync(ct).ConfigureAwait(false);
        if (!response.IsSuccessStatusCode)
        {
            throw MogeniusErrors.FromResponse((int)response.StatusCode, text,
                $"{method.Method} {path} → HTTP {(int)response.StatusCode}");
        }

        if (string.IsNullOrEmpty(text))
        {
            return default!;
        }

        try
        {
            return JsonSerializer.Deserialize<T>(text, Json)!;
        }
        catch (JsonException err)
        {
            throw new MogeniusException($"{method.Method} {path}: the platform answered with unexpected JSON: {err.Message}",
                (int)response.StatusCode, source: ErrorSource.Api, innerException: err);
        }
    }

    private async Task<HttpResponseMessage> SendAsync(HttpMethod method, string path, Query? query,
        HttpContent? content, CancellationToken ct)
    {
        var url = AppendQuery($"{_options.ApiUrl}{path}", query);
        using var request = new HttpRequestMessage(method, url) { Content = content };
        request.Headers.TryAddWithoutValidation("authorization", $"Bearer {_options.ApiKey}");
        request.Headers.TryAddWithoutValidation("accept", "application/json");
        request.Headers.TryAddWithoutValidation("user-agent", UserAgent);
        if (!string.IsNullOrEmpty(_options.OrganizationId))
        {
            request.Headers.TryAddWithoutValidation("organization-id", _options.OrganizationId);
        }

        if (!string.IsNullOrEmpty(_options.ClusterId))
        {
            request.Headers.TryAddWithoutValidation("cluster-id", _options.ClusterId);
        }

        if (!string.IsNullOrEmpty(_options.WorkspaceName))
        {
            request.Headers.TryAddWithoutValidation("workspace-name", _options.WorkspaceName);
        }

        try
        {
            // headers first: an upload's answer may come before its body is through
            return await _http.SendAsync(request, HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false);
        }
        catch (HttpRequestException err)
        {
            throw new MogeniusException($"{method.Method} {new Uri(url).AbsolutePath} failed: {err.Message}",
                source: ErrorSource.Sdk, innerException: err);
        }
    }

    /// <summary>Appends the set values of <paramref name="query"/> to <paramref name="url"/>, which may have a query already.</summary>
    internal static string AppendQuery(string url, Query? query)
    {
        if (query is null)
        {
            return url;
        }

        var builder = new StringBuilder(url);
        var separator = url.Contains('?') ? '&' : '?';
        foreach (var (key, value) in query)
        {
            if (string.IsNullOrEmpty(value))
            {
                continue;
            }

            builder.Append(separator).Append(Uri.EscapeDataString(key)).Append('=')
                .Append(Uri.EscapeDataString(value));
            separator = '&';
        }

        return builder.ToString();
    }

    public void Dispose()
    {
        if (_ownsHttp)
        {
            _http.Dispose();
        }
    }
}

/// <summary>Query parameters; null or empty values are left out.</summary>
internal sealed class Query : Dictionary<string, string?>;
