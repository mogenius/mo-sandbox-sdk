using System.Buffers;
using System.Collections.Concurrent;
using System.IO.Pipelines;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading.Channels;

namespace Mogenius.Sandboxes;

/// <summary>Where a tunnel leads: a port of a pod, or with mode ssh the cluster's SSH gateway for the pod.</summary>
internal sealed record TunnelTarget(string Namespace, string PodName, int? Port = null, string? Mode = null);

/// <summary>
/// mogenius: a local TCP port forwarded to a port of the sandbox through the platform's stream gateway, from
/// <c>Sandbox.TunnelAsync()</c>. Only bytes travel: the sandbox sees no credentials. When the gateway connection
/// drops, the tunnel reconnects on its own; connections open at that moment break. Dispose it when done.
/// </summary>
public sealed class Tunnel : IAsyncDisposable
{
    // Frames of a port-forward stream. Each TCP connection through the tunnel has an id: text frames open and close
    // it, binary frames carry its bytes behind a one-byte id length and the id.
    private const string OpenPrefix = "PFM:O:";
    private const string ClosePrefix = "PFM:C:";
    private const string Ready = "PEER_IS_READY";
    private const string ReadyAck = "ack-ready";
    private const string Ping = "BROWSER_PING";
    private const string PeerClosed = "CLOSE_CONNECTION_FROM_PEER";

    private static readonly TimeSpan PingInterval = TimeSpan.FromSeconds(5);
    private static readonly TimeSpan ReadyTimeout = TimeSpan.FromSeconds(30);
    private const int ReadBufferBytes = 32 * 1024;

    /// <summary>Pauses before each attempt to reconnect, the last one repeating; tests shorten them.</summary>
    internal static TimeSpan[] ReconnectDelays =
    [
        TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(4), TimeSpan.FromSeconds(8),
        TimeSpan.FromSeconds(16), TimeSpan.FromSeconds(30),
    ];

    private readonly ApiClient _api;
    private readonly TunnelTarget _target;
    private readonly TaskCompletionSource<Exception?> _done = new(TaskCreationOptions.RunContinuationsAsynchronously);
    private readonly CancellationTokenSource _ended = new();
    private readonly ConcurrentDictionary<string, Connection> _connections = new();
    private TcpListener? _listener;
    /// <summary>The live gateway stream; null while reconnecting.</summary>
    private volatile GatewaySocket? _socket;
    /// <summary>A stream still waiting for the operator, so closing can abort it.</summary>
    private volatile GatewaySocket? _connecting;
    private int _sequence;
    private int _endedFlag;

    internal Tunnel(ApiClient api, TunnelTarget target)
    {
        _api = api;
        _target = target;
    }

    /// <summary>Always <c>127.0.0.1</c>.</summary>
    public string Host => "127.0.0.1";

    /// <summary>The local port that leads into the sandbox; 0 for a tunnel without a listener.</summary>
    public int Port { get; private set; }

    /// <summary><c>http://127.0.0.1:&lt;port&gt;</c>, for an HTTP service behind the tunnel.</summary>
    public string Url => $"http://{Host}:{Port}";

    /// <summary>Completes when the tunnel has ended: with the error that ended it, or null after closing.</summary>
    public Task<Exception?> Completion => _done.Task;

    /// <summary>
    /// Opens a tunnel: waits until the operator holds the pod's port, then listens on 127.0.0.1 at
    /// <paramref name="localPort"/> (0: a free one; null: no listener, connections only through
    /// <see cref="CreateConnection"/>).
    /// </summary>
    internal static async Task<Tunnel> OpenAsync(ApiClient api, TunnelTarget target, int? localPort,
        CancellationToken ct)
    {
        var tunnel = new Tunnel(api, target);
        await tunnel.StartAsync(localPort, ct).ConfigureAwait(false);
        return tunnel;
    }

    private async Task StartAsync(int? localPort, CancellationToken ct)
    {
        var socket = await ConnectAsync(ct).ConfigureAwait(false);
        _socket = socket;
        _ = ServeAsync(socket);
        if (localPort is { } port)
        {
            var listener = new TcpListener(IPAddress.Loopback, port);
            try
            {
                listener.Start();
            }
            catch (SocketException err)
            {
                End(null);
                throw new MogeniusException($"Could not listen on {Host}:{port}: {err.Message}",
                    source: ErrorSource.Sdk, innerException: err);
            }

            _listener = listener;
            Port = ((IPEndPoint)listener.LocalEndpoint).Port;
            _ = AcceptAsync(listener);
        }

        _ = PingAsync();
    }

    /// <summary>
    /// A connection to the sandbox's port without the local listener — for an HTTP, WebSocket or database client
    /// in the same process. It behaves like a TCP socket: disposing one side ends the other. Throws while the
    /// tunnel is reconnecting or after it ended.
    /// </summary>
    public Stream CreateConnection()
    {
        if (Volatile.Read(ref _endedFlag) == 1)
        {
            throw new MogeniusException("The tunnel is closed.", source: ErrorSource.Sdk);
        }

        if (_socket is not { IsOpen: true })
        {
            throw new MogeniusException("The tunnel is reconnecting to the stream gateway; try again in a moment.",
                source: ErrorSource.Sdk);
        }

        var toSandbox = new Pipe();
        var fromSandbox = new Pipe();
        var outside = new DuplexPipeStream(fromSandbox.Reader, toSandbox.Writer);
        var inside = new DuplexPipeStream(toSandbox.Reader, fromSandbox.Writer);
        Bridge(inside);
        return outside;
    }

    /// <summary>Ends the tunnel and every connection through it.</summary>
    public async Task CloseAsync()
    {
        End(null);
        await _done.Task.ConfigureAwait(false);
    }

    /// <inheritdoc cref="CloseAsync"/>
    public async ValueTask DisposeAsync() => await CloseAsync().ConfigureAwait(false);

    /// <summary>Opens the gateway stream and waits until the operator holds the port.</summary>
    private async Task<GatewaySocket> ConnectAsync(CancellationToken ct)
    {
        var query = new Query
        {
            ["type"] = "PORT_FORWARD",
            ["cmd"] = "port-forward",
            ["namespace"] = _target.Namespace,
            ["kind"] = "Pod",
            ["workloadName"] = _target.PodName,
            ["remotePort"] = _target.Port?.ToString(System.Globalization.CultureInfo.InvariantCulture),
            ["mode"] = _target.Mode,
        };
        using var wait = CancellationTokenSource.CreateLinkedTokenSource(ct, _ended.Token);
        wait.CancelAfter(ReadyTimeout);
        var socket = await _api.OpenStreamAsync(query, wait.Token).ConfigureAwait(false);
        _connecting = socket;
        try
        {
            while (true)
            {
                GatewayFrame? frame;
                try
                {
                    frame = await socket.ReceiveAsync(wait.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException) when (!ct.IsCancellationRequested && !_ended.IsCancellationRequested)
                {
                    frame = null;
                }

                if (frame is null && wait.IsCancellationRequested && !ct.IsCancellationRequested &&
                    !_ended.IsCancellationRequested)
                {
                    throw new MogeniusTimeoutException(
                        $"The cluster operator did not open the tunnel within {ReadyTimeout.TotalSeconds} s.",
                        source: ErrorSource.Sdk);
                }

                ct.ThrowIfCancellationRequested();
                if (frame is not { } received)
                {
                    throw socket.CloseCode == 1000 && string.IsNullOrEmpty(socket.CloseReason)
                        ? new MogeniusException("The stream gateway closed the tunnel before the operator opened it.",
                            source: ErrorSource.Api)
                        : StreamErrors.FromClose(socket.CloseCode, socket.CloseReason, _api.StreamUrl);
                }

                if (!received.IsText)
                {
                    continue;
                }

                var text = received.Text;
                if (text is Ready or ReadyAck)
                {
                    await socket.SendTextAsync(Ready, wait.Token).ConfigureAwait(false);
                    return socket;
                }

                if (text.Contains("DOES_NOT_EXIST", StringComparison.Ordinal))
                {
                    throw new MogeniusNotFoundException(text, source: ErrorSource.Operator);
                }

                if (text.Contains("UNAUTHORIZED", StringComparison.Ordinal))
                {
                    throw new MogeniusForbiddenException(text, source: ErrorSource.Api);
                }

                if (text.Contains("ERROR", StringComparison.Ordinal))
                {
                    throw new MogeniusException(text, source: ErrorSource.Operator);
                }
                // anything else (pings) is the gateway talking to itself
            }
        }
        catch
        {
            await socket.DisposeAsync().ConfigureAwait(false);
            throw;
        }
        finally
        {
            _connecting = null;
        }
    }

    /// <summary>Hands each frame of the gateway stream to its connection; a broken stream is reconnected.</summary>
    private async Task ServeAsync(GatewaySocket socket)
    {
        try
        {
            while (true)
            {
                GatewayFrame? received;
                try
                {
                    received = await socket.ReceiveAsync(_ended.Token).ConfigureAwait(false);
                }
                catch (OperationCanceledException)
                {
                    return;
                }

                if (received is not { } frame)
                {
                    break;
                }

                if (!frame.IsText)
                {
                    var data = frame.Data;
                    var head = 1 + (data.Length > 0 ? data[0] : 0);
                    if (data.Length >= 2 && data.Length >= head &&
                        _connections.TryGetValue(Encoding.UTF8.GetString(data, 1, head - 1), out var connection))
                    {
                        connection.Deliver(data[head..]);
                    }

                    continue;
                }

                var text = frame.Text;
                if (text.StartsWith(ClosePrefix, StringComparison.Ordinal))
                {
                    CloseConnection(text[ClosePrefix.Length..], tell: false);
                }
                else if (text == PeerClosed)
                {
                    End(new MogeniusException("The stream gateway closed the tunnel.", source: ErrorSource.Api));
                    return;
                }
            }
        }
        finally
        {
            await socket.DisposeAsync().ConfigureAwait(false);
        }

        if (Volatile.Read(ref _endedFlag) == 1)
        {
            return;
        }

        // the connections of the broken stream are gone with it
        _socket = null;
        DropConnections();
        _ = ReconnectAsync();
    }

    private async Task ReconnectAsync()
    {
        for (var attempt = 0; ; attempt++)
        {
            try
            {
                await Task.Delay(ReconnectDelays[Math.Min(attempt, ReconnectDelays.Length - 1)], _ended.Token)
                    .ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                return;
            }

            GatewaySocket socket;
            try
            {
                socket = await ConnectAsync(CancellationToken.None).ConfigureAwait(false);
            }
            catch (MogeniusNotFoundException err)
            {
                // the pod is gone; under its name it does not come back
                End(err);
                return;
            }
            catch (Exception) when (Volatile.Read(ref _endedFlag) == 0)
            {
                continue;
            }
            catch (Exception)
            {
                return;
            }

            if (Volatile.Read(ref _endedFlag) == 1)
            {
                await socket.DisposeAsync().ConfigureAwait(false);
                return;
            }

            _socket = socket;
            _ = ServeAsync(socket);
            return;
        }
    }

    private async Task AcceptAsync(TcpListener listener)
    {
        while (!_ended.IsCancellationRequested)
        {
            TcpClient client;
            try
            {
                client = await listener.AcceptTcpClientAsync(_ended.Token).ConfigureAwait(false);
            }
            catch (Exception err) when (err is OperationCanceledException or SocketException
                                            or ObjectDisposedException)
            {
                return;
            }

            client.NoDelay = true;
            Bridge(client.GetStream());
        }
    }

    private async Task PingAsync()
    {
        using var timer = new PeriodicTimer(PingInterval);
        try
        {
            while (await timer.WaitForNextTickAsync(_ended.Token).ConfigureAwait(false))
            {
                if (_socket is { IsOpen: true } socket)
                {
                    await socket.TrySendTextAsync(Ping).ConfigureAwait(false);
                }
            }
        }
        catch (OperationCanceledException)
        {
            // the tunnel ended
        }
    }

    /// <summary>Announces a local connection to the gateway and pumps its bytes there.</summary>
    private void Bridge(Stream local)
    {
        var socket = _socket;
        if (socket is not { IsOpen: true })
        {
            local.Dispose();
            return;
        }

        var id = $"c{Interlocked.Increment(ref _sequence)}";
        var connection = new Connection(local);
        _connections[id] = connection;
        _ = PumpAsync(id, connection, socket);
    }

    private async Task PumpAsync(string id, Connection connection, GatewaySocket socket)
    {
        var idBytes = Encoding.UTF8.GetBytes(id);
        var buffer = ArrayPool<byte>.Shared.Rent(1 + idBytes.Length + ReadBufferBytes);
        try
        {
            await socket.SendTextAsync(OpenPrefix + id, _ended.Token).ConfigureAwait(false);
            buffer[0] = (byte)idBytes.Length;
            idBytes.CopyTo(buffer, 1);
            var head = 1 + idBytes.Length;
            while (true)
            {
                var read = await connection.Local.ReadAsync(buffer.AsMemory(head, ReadBufferBytes), _ended.Token)
                    .ConfigureAwait(false);
                if (read == 0)
                {
                    break;
                }

                // the send waits until the socket took the frame: a slow gateway holds the local side back
                await socket.SendBinaryAsync(buffer.AsMemory(0, head + read), _ended.Token).ConfigureAwait(false);
            }
        }
        catch (Exception)
        {
            // the local side or the gateway went away: the connection ends either way
        }
        finally
        {
            ArrayPool<byte>.Shared.Return(buffer);
            CloseConnection(id, tell: true);
        }
    }

    /// <summary>Ends one connection; <paramref name="tell"/> is whether the gateway still has to hear of it.</summary>
    private void CloseConnection(string id, bool tell)
    {
        if (!_connections.TryRemove(id, out var connection))
        {
            return;
        }

        if (tell)
        {
            connection.Destroy();
            if (_socket is { } socket)
            {
                _ = socket.TrySendTextAsync(ClosePrefix + id);
            }
        }
        else
        {
            // the pod closed it: what it sent before still reaches the local side
            connection.End();
        }
    }

    private void DropConnections()
    {
        foreach (var id in _connections.Keys)
        {
            if (_connections.TryRemove(id, out var connection))
            {
                connection.Destroy();
            }
        }
    }

    private void End(Exception? error)
    {
        if (Interlocked.Exchange(ref _endedFlag, 1) == 1)
        {
            return;
        }

        _ended.Cancel();
        DropConnections();
        _listener?.Stop();
        var sockets = new[] { _socket, _connecting };
        _socket = null;
        _ = Task.Run(async () =>
        {
            foreach (var socket in sockets)
            {
                if (socket is not null)
                {
                    await socket.DisposeAsync().ConfigureAwait(false);
                }
            }

            _done.TrySetResult(error);
        });
    }

    /// <summary>
    /// One local connection: bytes from the sandbox queue up and are written in order, so a slow local reader holds
    /// back only its own connection.
    /// </summary>
    private sealed class Connection
    {
        private readonly Channel<byte[]> _outbox = Channel.CreateUnbounded<byte[]>(
            new UnboundedChannelOptions { SingleReader = true });

        public Connection(Stream local)
        {
            Local = local;
            _ = WriteAsync();
        }

        public Stream Local { get; }

        public void Deliver(byte[] data) => _outbox.Writer.TryWrite(data);

        /// <summary>Writes what is queued, then closes the local side.</summary>
        public void End() => _outbox.Writer.TryComplete();

        /// <summary>Closes the local side now.</summary>
        public void Destroy()
        {
            _outbox.Writer.TryComplete();
            Local.Dispose();
        }

        private async Task WriteAsync()
        {
            try
            {
                await foreach (var data in _outbox.Reader.ReadAllAsync().ConfigureAwait(false))
                {
                    await Local.WriteAsync(data).ConfigureAwait(false);
                    await Local.FlushAsync().ConfigureAwait(false);
                }
            }
            catch (Exception)
            {
                // the local side went away
            }
            finally
            {
                await Local.DisposeAsync().ConfigureAwait(false);
            }
        }
    }
}

/// <summary>One end of an in-process connection: reads one pipe, writes the other; disposing ends both directions.</summary>
internal sealed class DuplexPipeStream(PipeReader reader, PipeWriter writer) : Stream
{
    private readonly Stream _in = reader.AsStream();
    private readonly Stream _out = writer.AsStream();

    public override bool CanRead => true;
    public override bool CanWrite => true;
    public override bool CanSeek => false;
    public override long Length => throw new NotSupportedException();

    public override long Position
    {
        get => throw new NotSupportedException();
        set => throw new NotSupportedException();
    }

    public override int Read(byte[] buffer, int offset, int count) => _in.Read(buffer, offset, count);

    public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default) =>
        _in.ReadAsync(buffer, cancellationToken);

    public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
        _in.ReadAsync(buffer, offset, count, cancellationToken);

    public override void Write(byte[] buffer, int offset, int count) => _out.Write(buffer, offset, count);

    public override ValueTask WriteAsync(ReadOnlyMemory<byte> buffer, CancellationToken cancellationToken = default) =>
        _out.WriteAsync(buffer, cancellationToken);

    public override Task WriteAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
        _out.WriteAsync(buffer, offset, count, cancellationToken);

    public override void Flush() => _out.Flush();
    public override Task FlushAsync(CancellationToken cancellationToken) => _out.FlushAsync(cancellationToken);
    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            _in.Dispose();
            _out.Dispose();
        }

        base.Dispose(disposing);
    }
}
