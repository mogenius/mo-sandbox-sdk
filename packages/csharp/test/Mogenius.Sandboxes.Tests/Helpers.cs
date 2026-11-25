using System.Collections.Concurrent;
using System.Net;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Threading.Channels;

namespace Mogenius.Sandboxes.Tests;

/// <summary>One recorded request of the fake platform.</summary>
public sealed record RecordedCall(HttpMethod Method, Uri Url, Dictionary<string, string> Headers, string? ContentType,
    byte[] Body)
{
    public string BodyText => Encoding.UTF8.GetString(Body);
    public JsonElement Json => JsonDocument.Parse(Body).RootElement;
    public string? Query(string name) => System.Web.HttpUtility.ParseQueryString(Url.Query)[name];
}

/// <summary>A canned answer: status and JSON body, or a prepared response.</summary>
public sealed record Answer(int Status = 200, object? Body = null, Func<HttpResponseMessage>? Response = null);

/// <summary>
/// A platform that answers from a queue and records what it was asked. Answers are consumed in order; the last one
/// repeats so polling loops have something to read.
/// </summary>
public sealed class FakePlatform : HttpMessageHandler
{
    private readonly Queue<Func<RecordedCall, Answer>> _answers = new();
    private Func<RecordedCall, Answer>? _last;

    public List<RecordedCall> Calls { get; } = [];

    public FakePlatform Answer(int status = 200, object? body = null)
    {
        Enqueue(_ => new Answer(status, body));
        return this;
    }

    public FakePlatform Answer(Func<RecordedCall, Answer> answer)
    {
        Enqueue(answer);
        return this;
    }

    private void Enqueue(Func<RecordedCall, Answer> answer)
    {
        _answers.Enqueue(answer);
        _last = answer;
    }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        var headers = request.Headers.ToDictionary(h => h.Key.ToLowerInvariant(), h => string.Join(",", h.Value));
        var body = request.Content is null ? [] : await request.Content.ReadAsByteArrayAsync(cancellationToken);
        var call = new RecordedCall(request.Method, request.RequestUri!, headers,
            request.Content?.Headers.ContentType?.ToString(), body);
        lock (Calls)
        {
            Calls.Add(call);
        }

        var next = _answers.Count > 0 ? _answers.Dequeue() : _last ?? (_ => new Answer());
        var answer = next(call);
        if (answer.Response is not null)
        {
            return answer.Response();
        }

        var response = new HttpResponseMessage((HttpStatusCode)answer.Status);
        if (answer.Body is not null)
        {
            response.Content = new StringContent(JsonSerializer.Serialize(answer.Body), Encoding.UTF8,
                "application/json");
        }

        return response;
    }
}

/// <summary>A WebSocket the test drives: it records what was sent and lets the test play the gateway's frames.</summary>
public sealed class FakeWebSocket(Uri url) : WebSocket
{
    private readonly Channel<(WebSocketMessageType Type, byte[] Data, int Code, string? Reason)> _inbound =
        Channel.CreateUnbounded<(WebSocketMessageType, byte[], int, string?)>();

    private WebSocketState _state = WebSocketState.Open;
    private WebSocketCloseStatus? _closeStatus;
    private string? _closeDescription;

    public Uri Url { get; } = url;
    public ConcurrentQueue<string> SentText { get; } = new();
    public ConcurrentQueue<byte[]> SentBinary { get; } = new();
    public int? ClosedWith { get; private set; }
    public event Action<string>? OnText;

    public string? Query(string name) => System.Web.HttpUtility.ParseQueryString(Url.Query)[name];

    /// <summary>The gateway sends a text frame.</summary>
    public void Frame(string text) =>
        _inbound.Writer.TryWrite((WebSocketMessageType.Text, Encoding.UTF8.GetBytes(text), 0, null));

    /// <summary>The gateway sends a binary frame.</summary>
    public void Frame(byte[] data) => _inbound.Writer.TryWrite((WebSocketMessageType.Binary, data, 0, null));

    /// <summary>The gateway closes.</summary>
    public void End(int code, string reason = "") =>
        _inbound.Writer.TryWrite((WebSocketMessageType.Close, [], code, reason));

    public override WebSocketCloseStatus? CloseStatus => _closeStatus;
    public override string? CloseStatusDescription => _closeDescription;
    public override WebSocketState State => _state;
    public override string? SubProtocol => null;

    public override void Abort() => _state = WebSocketState.Aborted;

    public override Task CloseAsync(WebSocketCloseStatus closeStatus, string? statusDescription,
        CancellationToken cancellationToken) => CloseOutputAsync(closeStatus, statusDescription, cancellationToken);

    public override Task CloseOutputAsync(WebSocketCloseStatus closeStatus, string? statusDescription,
        CancellationToken cancellationToken)
    {
        ClosedWith ??= (int)closeStatus;
        _state = WebSocketState.Closed;
        _inbound.Writer.TryComplete();
        return Task.CompletedTask;
    }

    public override void Dispose() => _state = WebSocketState.Closed;

    public override async Task<WebSocketReceiveResult> ReceiveAsync(ArraySegment<byte> buffer,
        CancellationToken cancellationToken)
    {
        (WebSocketMessageType Type, byte[] Data, int Code, string? Reason) item;
        try
        {
            item = await _inbound.Reader.ReadAsync(cancellationToken);
        }
        catch (ChannelClosedException)
        {
            throw new WebSocketException(WebSocketError.ConnectionClosedPrematurely);
        }

        if (item.Type == WebSocketMessageType.Close)
        {
            _state = WebSocketState.CloseReceived;
            _closeStatus = (WebSocketCloseStatus)item.Code;
            _closeDescription = item.Reason;
            return new WebSocketReceiveResult(0, WebSocketMessageType.Close, true, _closeStatus, _closeDescription);
        }

        item.Data.CopyTo(buffer.Array!, buffer.Offset);
        return new WebSocketReceiveResult(item.Data.Length, item.Type, true);
    }

    public override Task SendAsync(ArraySegment<byte> buffer, WebSocketMessageType messageType, bool endOfMessage,
        CancellationToken cancellationToken)
    {
        if (_state != WebSocketState.Open)
        {
            throw new WebSocketException(WebSocketError.InvalidState);
        }

        var data = buffer.ToArray();
        if (messageType == WebSocketMessageType.Text)
        {
            var text = Encoding.UTF8.GetString(data);
            SentText.Enqueue(text);
            OnText?.Invoke(text);
        }
        else
        {
            SentBinary.Enqueue(data);
        }

        return Task.CompletedTask;
    }
}

public static class Fixtures
{
    public static Dictionary<string, object?> SandboxInfo(params (string Key, object? Value)[] overrides)
    {
        var info = new Dictionary<string, object?>
        {
            ["id"] = "default-abc12",
            ["name"] = "default-abc12",
            ["namespace"] = "agent-sandbox",
            ["kind"] = "SandboxClaim",
            ["state"] = "started",
            ["stateReason"] = null,
            ["labels"] = new Dictionary<string, string>(),
            ["profile"] = "default",
            ["image"] = "ghcr.io/mogenius/agent-sandbox-chart/sandbox-default:1.2.0",
            ["podName"] = "default-k27tp",
            ["sandboxName"] = "default-k27tp",
            ["containerName"] = "sandbox",
            ["serviceFQDN"] = "default-k27tp.agent-sandbox.svc.cluster.local",
            ["operatingMode"] = "Running",
            ["ephemeral"] = true,
            ["expiresAt"] = null,
            ["createdAt"] = "2026-10-01T12:00:00.000Z",
            ["createdBy"] = "jane@example.com",
        };
        foreach (var (key, value) in overrides)
        {
            info[key] = value;
        }

        return info;
    }

    public static readonly List<FakeWebSocket> NoSockets = [];

    public static MogeniusClient Client(FakePlatform platform, List<FakeWebSocket>? sockets = null,
        Action<MogeniusOptions>? configure = null)
    {
        var options = new MogeniusOptions
        {
            ApiKey = "mo_pat:user:secret",
            ApiUrl = "https://api.test/",
            OrganizationId = "org-1",
            ClusterId = "cluster-1",
            StreamUrl = "wss://stream.test",
            HttpClient = new HttpClient(platform),
            WebSocketFactory = (uri, _) =>
            {
                var socket = new FakeWebSocket(uri);
                lock (sockets ?? NoSockets)
                {
                    (sockets ?? NoSockets).Add(socket);
                }

                return Task.FromResult<WebSocket>(socket);
            },
        };
        configure?.Invoke(options);
        return new MogeniusClient(options);
    }

    /// <summary>A started sandbox from a client whose platform answers the get first.</summary>
    public static async Task<Sandbox> Sandbox(FakePlatform platform, List<FakeWebSocket>? sockets = null)
    {
        var client = Client(platform.Answer(body: SandboxInfo()), sockets);
        var sandbox = await client.GetAsync("default-abc12");
        platform.Calls.Clear();
        return sandbox;
    }

    /// <summary>A binary stream frame: the tag byte and the text behind it.</summary>
    public static byte[] StreamBytes(byte tag, string text) => [tag, .. Encoding.UTF8.GetBytes(text)];

    public static async Task<T> Eventually<T>(Func<T?> probe, int timeoutMs = 3000) where T : class
    {
        var deadline = DateTime.UtcNow.AddMilliseconds(timeoutMs);
        while (DateTime.UtcNow < deadline)
        {
            if (probe() is { } value)
            {
                return value;
            }

            await Task.Delay(10);
        }

        throw new TimeoutException("condition not met in time");
    }
}
