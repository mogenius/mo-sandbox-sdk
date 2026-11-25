using System.Text.Json;

namespace Mogenius.Sandboxes;

/// <summary>
/// Entry point:
/// <code>
/// using var mogenius = new MogeniusClient();                 // MOGENIUS_API_KEY etc. from the environment
/// var sandbox = await mogenius.CreateAsync();                // claimed from the default warm pool, started
/// var result = await sandbox.Process.ExecuteCommandAsync("echo hello");
/// await mogenius.DeleteAsync(sandbox);
/// </code>
/// Sandboxes run as pods in your own cluster, under your RBAC, through the mogenius operator; nothing leaves the
/// cluster but the API calls.
/// </summary>
public sealed class MogeniusClient : IDisposable
{
    /// <summary>The platform caps its own wait for a start at this.</summary>
    private const int PlatformWaitCapSeconds = 300;

    private static readonly TimeSpan DefaultCreateTimeout = TimeSpan.FromSeconds(60);

    private readonly ApiClient _api;
    private readonly string _namespace;
    /// <summary>The warm pool <see cref="CreateAsync"/> claims from when the params name none.</summary>
    private readonly string _profile;

    /// <summary>A client from <paramref name="options"/>; what they leave out comes from the environment.</summary>
    public MogeniusClient(MogeniusOptions? options = null)
    {
        var resolved = ResolvedOptions.Resolve(options);
        _api = new ApiClient(resolved);
        _namespace = resolved.Namespace;
        _profile = resolved.Profile;
    }

    /// <summary>
    /// Creates a sandbox and, by default, waits until it is started. Without <see cref="CreateSandboxParams.Image"/>
    /// it is claimed from the profile's warm pool and ready in seconds; with an image a pod is started from the
    /// profile's template with that image. Without <see cref="CreateSandboxParams.Snapshot"/> the profile is the
    /// configured one (<c>default</c> unless set).
    /// </summary>
    /// <param name="parameters">what to create</param>
    /// <param name="timeout">how long to wait for the start (default 60 s); <see cref="TimeSpan.Zero"/> returns right after creation</param>
    /// <param name="cancellationToken">cancels the request and the wait</param>
    public async Task<Sandbox> CreateAsync(CreateSandboxParams? parameters = null, TimeSpan? timeout = null,
        CancellationToken cancellationToken = default)
    {
        parameters ??= new CreateSandboxParams();
        var wait = timeout ?? DefaultCreateTimeout;
        var body = new Dictionary<string, object?>();
        if (!string.IsNullOrEmpty(parameters.Name))
        {
            body["name"] = parameters.Name;
        }

        body["profile"] = string.IsNullOrEmpty(parameters.Snapshot) ? _profile : parameters.Snapshot;
        if (!string.IsNullOrEmpty(parameters.Image))
        {
            body["image"] = parameters.Image;
        }

        if (parameters.Labels is not null)
        {
            body["labels"] = parameters.Labels;
        }

        if (parameters.EnvVars is { Count: > 0 })
        {
            body["env"] = parameters.EnvVars;
        }

        if (parameters.AutoDeleteInterval is > 0 and var ttl)
        {
            body["ttlMinutes"] = (int)Math.Ceiling(ttl);
        }

        if (parameters.AutoStopInterval is > 0 and var autoStop)
        {
            body["autoStopInterval"] = (int)Math.Ceiling(autoStop);
        }

        if (parameters.Ephemeral is { } ephemeral)
        {
            body["ephemeral"] = ephemeral;
        }

        body["waitForStart"] = wait > TimeSpan.Zero;
        if (wait > TimeSpan.Zero)
        {
            body["waitTimeoutSeconds"] = (int)Math.Min(PlatformWaitCapSeconds, Math.Ceiling(wait.TotalSeconds));
        }

        var info = await _api.PostAsync<SandboxInfo>($"/sandbox/{Uri.EscapeDataString(_namespace)}", body,
            ct: cancellationToken).ConfigureAwait(false);
        var sandbox = new Sandbox(_api, info, parameters.Language ?? CodeLanguage.Python);
        if (wait > TimeSpan.Zero && sandbox.State != SandboxState.Started)
        {
            // the platform waited as long as it may; finish the wait here with what is left
            var left = Math.Max(1, wait.TotalSeconds - PlatformWaitCapSeconds);
            await sandbox.WaitUntilStartedAsync(TimeSpan.FromSeconds(left), cancellationToken).ConfigureAwait(false);
        }

        return sandbox;
    }

    /// <summary>The sandbox with that id (claim or sandbox name).</summary>
    public async Task<Sandbox> GetAsync(string sandboxId, CodeLanguage language = CodeLanguage.Python,
        CancellationToken cancellationToken = default)
    {
        var info = await _api.GetAsync<SandboxInfo>(
            $"/sandbox/{Uri.EscapeDataString(_namespace)}/{Uri.EscapeDataString(sandboxId)}",
            ct: cancellationToken).ConfigureAwait(false);
        return new Sandbox(_api, info, language);
    }

    /// <summary>The first sandbox matching the filter; by id when given, else by labels and state.</summary>
    public async Task<Sandbox> FindOneAsync(SandboxFilter filter, CancellationToken cancellationToken = default)
    {
        if (!string.IsNullOrEmpty(filter.Id))
        {
            return await GetAsync(filter.Id, cancellationToken: cancellationToken).ConfigureAwait(false);
        }

        var page = await ListAsync(filter.Labels, 1, null, filter.State, cancellationToken).ConfigureAwait(false);
        return page.Items.Count > 0
            ? page.Items[0]
            : throw new MogeniusNotFoundException(
                $"No sandbox matches {JsonSerializer.Serialize(filter, ApiClient.Json)} in namespace \"{_namespace}\".",
                errorCode: "SANDBOX_NOT_FOUND");
    }

    /// <summary>Sandboxes in the namespace, optionally filtered by labels and state, one page at a time.</summary>
    /// <param name="labels">all of these labels</param>
    /// <param name="limit">page size</param>
    /// <param name="cursor">the previous page's <see cref="PaginatedSandboxes.NextCursor"/></param>
    /// <param name="state">only sandboxes in this state</param>
    /// <param name="cancellationToken">cancels the request</param>
    public async Task<PaginatedSandboxes> ListAsync(IReadOnlyDictionary<string, string>? labels = null,
        int? limit = null, string? cursor = null, SandboxState? state = null,
        CancellationToken cancellationToken = default)
    {
        var body = await _api.GetAsync<ListResponseBody>($"/sandbox/{Uri.EscapeDataString(_namespace)}", new Query
        {
            ["labels"] = labels is { Count: > 0 }
                ? string.Join(',', labels.Select(label => $"{label.Key}={label.Value}"))
                : null,
            ["state"] = state is { } value ? Sandbox.StateName(value) : null,
            ["limit"] = limit?.ToString(System.Globalization.CultureInfo.InvariantCulture),
            ["cursor"] = cursor,
        }, cancellationToken).ConfigureAwait(false);
        return new PaginatedSandboxes(
            (body.Items ?? []).Select(info => new Sandbox(_api, info)).ToList(), body.NextCursor, body.TotalCount);
    }

    /// <summary>
    /// Warm pools of the namespace (mogenius), or of every namespace with <paramref name="allNamespaces"/>, sorted
    /// by namespace and name. A cluster without the agent-sandbox resource types has none: an empty list, not an
    /// error.
    /// </summary>
    public async Task<IReadOnlyList<WarmPool>> WarmPoolsAsync(bool allNamespaces = false,
        CancellationToken cancellationToken = default)
    {
        JsonElement body;
        try
        {
            // the SandboxWarmPool type as the platform's generic resource routes name it; the objects' data is left
            // out. Without a namespace the platform lists every namespace.
            body = await _api.GetAsync<JsonElement>("/resource/workload/list", new Query
            {
                ["kind"] = "SandboxWarmPool",
                ["plural"] = "sandboxwarmpools",
                ["apiVersion"] = "extensions.agents.x-k8s.io/v1beta1",
                ["namespaced"] = "true",
                ["namespace"] = allNamespaces ? null : _namespace,
                ["withData"] = "false",
            }, cancellationToken).ConfigureAwait(false);
        }
        catch (MogeniusException err) when (err is MogeniusNotFoundException ||
                                            (err.StatusCode == 400 && IsMissingTypeMessage(err.Message)))
        {
            return [];
        }

        if (body.ValueKind == JsonValueKind.Object && String(body, "status") == "error")
        {
            var message = String(body, "message");
            var statusCode = body.TryGetProperty("statusCode", out var code) && code.ValueKind == JsonValueKind.Number
                ? code.GetInt32()
                : (int?)null;
            if (statusCode == 404 || IsMissingTypeMessage(message))
            {
                return [];
            }

            throw new MogeniusException($"listing warm pools: {message ?? "unknown error"}", statusCode,
                source: ErrorSource.Operator);
        }

        // the operator's answer as the platform relays it: an UnstructuredList in data (data.items), or a bare list
        var items = body.ValueKind == JsonValueKind.Object && body.TryGetProperty("data", out var data)
            ? data.ValueKind switch
            {
                JsonValueKind.Array => data,
                JsonValueKind.Object when data.TryGetProperty("items", out var inner) &&
                                          inner.ValueKind == JsonValueKind.Array => inner,
                _ => default,
            }
            : default;
        if (items.ValueKind != JsonValueKind.Array)
        {
            return [];
        }

        return items.EnumerateArray()
            .Select(pool => new WarmPool(
                Name: String(pool, "metadata", "name") ?? "",
                Namespace: String(pool, "metadata", "namespace") ?? "",
                Template: String(pool, "spec", "sandboxTemplateRef", "name") ?? "",
                Ready: Int(pool, "status", "readyReplicas"),
                Size: Int(pool, "spec", "replicas")))
            // by code point, as the Go SDK sorts
            .OrderBy(pool => pool.Namespace, StringComparer.Ordinal)
            .ThenBy(pool => pool.Name, StringComparer.Ordinal)
            .ToList();
    }

    /// <summary>Starts a stopped sandbox.</summary>
    public Task StartAsync(Sandbox sandbox, TimeSpan? timeout = null, CancellationToken cancellationToken = default) =>
        sandbox.StartAsync(timeout, cancellationToken);

    /// <summary>Stops a running sandbox; the volume stays.</summary>
    public Task StopAsync(Sandbox sandbox, TimeSpan? timeout = null, CancellationToken cancellationToken = default) =>
        sandbox.StopAsync(timeout, cancellationToken);

    /// <summary>Deletes a sandbox.</summary>
    public Task DeleteAsync(Sandbox sandbox, CancellationToken cancellationToken = default) =>
        sandbox.DeleteAsync(cancellationToken);

    /// <summary>Snapshot service. Profiles are managed on the cluster's Sandboxes page for now.</summary>
    public object Snapshot => throw new MogeniusUnsupportedException("Managing snapshots from the SDK",
        "Use the profile name as `Snapshot` in CreateAsync(); profiles are configured on the Sandboxes page.");

    /// <summary>Volume service. Each sandbox has its own volume from the profile.</summary>
    public object Volume => throw new MogeniusUnsupportedException("Shared volumes",
        "Each sandbox gets its own persistent volume from the profile.");

    /// <summary>Releases the HTTP client the SDK created; one passed in the options stays the caller's.</summary>
    public void Dispose() => _api.Dispose();

    /// <summary>Kubernetes' answers for a resource type the cluster does not know (agent-sandbox not installed).</summary>
    private static bool IsMissingTypeMessage(string? message)
    {
        var text = (message ?? "").ToLowerInvariant();
        return text.Contains("could not find the requested resource", StringComparison.Ordinal) ||
               text.Contains("no matches for kind", StringComparison.Ordinal) ||
               text.Contains("doesn't have a resource type", StringComparison.Ordinal);
    }

    private static string? String(JsonElement element, params string[] path)
    {
        foreach (var key in path)
        {
            if (element.ValueKind != JsonValueKind.Object || !element.TryGetProperty(key, out element))
            {
                return null;
            }
        }

        return element.ValueKind == JsonValueKind.String ? element.GetString() : null;
    }

    private static int Int(JsonElement element, params string[] path)
    {
        foreach (var key in path)
        {
            if (element.ValueKind != JsonValueKind.Object || !element.TryGetProperty(key, out element))
            {
                return 0;
            }
        }

        return element.ValueKind == JsonValueKind.Number && element.TryGetInt32(out var value) ? value : 0;
    }

    private sealed record ListResponseBody(List<SandboxInfo>? Items, string? NextCursor, int TotalCount);
}
