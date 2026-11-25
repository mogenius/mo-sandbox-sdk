using System.Buffers;
using System.Net.WebSockets;
using System.Text;
using System.Text.RegularExpressions;

namespace Mogenius.Sandboxes;

/// <summary>One frame of the stream gateway: text or bytes.</summary>
internal readonly record struct GatewayFrame(bool IsText, byte[] Data)
{
    public string Text => Encoding.UTF8.GetString(Data);
}

/// <summary>
/// A socket on the platform's stream gateway: whole frames in, sends serialized (a WebSocket takes one send at a
/// time), and after the last frame the close code and reason — 1006 when the connection broke without a close.
/// </summary>
internal sealed class GatewaySocket(WebSocket socket) : IAsyncDisposable
{
    private readonly SemaphoreSlim _sendLock = new(1, 1);
    private int _disposed;

    /// <summary>Close code after <see cref="ReceiveAsync"/> returned null.</summary>
    public int CloseCode { get; private set; } = 1006;

    /// <summary>Close reason after <see cref="ReceiveAsync"/> returned null.</summary>
    public string? CloseReason { get; private set; }

    public bool IsOpen => socket.State == WebSocketState.Open;

    /// <summary>The next whole frame, or null once the socket is closed.</summary>
    public async Task<GatewayFrame?> ReceiveAsync(CancellationToken ct = default)
    {
        var buffer = ArrayPool<byte>.Shared.Rent(64 * 1024);
        try
        {
            using var message = new MemoryStream();
            while (true)
            {
                ValueWebSocketReceiveResult result;
                try
                {
                    result = await socket.ReceiveAsync(buffer.AsMemory(), ct).ConfigureAwait(false);
                }
                catch (Exception err) when (err is WebSocketException or ObjectDisposedException
                                                or InvalidOperationException)
                {
                    CloseCode = socket.CloseStatus is { } status ? (int)status : 1006;
                    CloseReason = socket.CloseStatusDescription;
                    return null;
                }

                if (result.MessageType == WebSocketMessageType.Close)
                {
                    CloseCode = socket.CloseStatus is { } status ? (int)status : 1005;
                    CloseReason = socket.CloseStatusDescription;
                    if (socket.State == WebSocketState.CloseReceived)
                    {
                        try
                        {
                            await socket.CloseOutputAsync(WebSocketCloseStatus.NormalClosure, null, CancellationToken.None)
                                .ConfigureAwait(false);
                        }
                        catch (Exception closeErr) when (closeErr is WebSocketException or ObjectDisposedException)
                        {
                            // already gone
                        }
                    }

                    return null;
                }

                message.Write(buffer, 0, result.Count);
                if (result.EndOfMessage)
                {
                    return new GatewayFrame(result.MessageType == WebSocketMessageType.Text, message.ToArray());
                }
            }
        }
        finally
        {
            ArrayPool<byte>.Shared.Return(buffer);
        }
    }

    public Task SendTextAsync(string text, CancellationToken ct = default) =>
        SendAsync(Encoding.UTF8.GetBytes(text), WebSocketMessageType.Text, ct);

    public Task SendBinaryAsync(ReadOnlyMemory<byte> data, CancellationToken ct = default) =>
        SendAsync(data, WebSocketMessageType.Binary, ct);

    /// <summary>Sends when the socket is open; a socket that went away meanwhile swallows the frame.</summary>
    public async Task<bool> TrySendTextAsync(string text, CancellationToken ct = default)
    {
        try
        {
            await SendTextAsync(text, ct).ConfigureAwait(false);
            return true;
        }
        catch (Exception err) when (err is WebSocketException or ObjectDisposedException or InvalidOperationException)
        {
            return false;
        }
    }

    private async Task SendAsync(ReadOnlyMemory<byte> data, WebSocketMessageType type, CancellationToken ct)
    {
        await _sendLock.WaitAsync(ct).ConfigureAwait(false);
        try
        {
            await socket.SendAsync(data, type, true, ct).ConfigureAwait(false);
        }
        finally
        {
            _sendLock.Release();
        }
    }

    /// <summary>Closes with 1000, if still open, without waiting long for the gateway's answer.</summary>
    public async Task CloseAsync()
    {
        if (socket.State is WebSocketState.Open or WebSocketState.CloseReceived)
        {
            using var timeout = new CancellationTokenSource(TimeSpan.FromSeconds(2));
            try
            {
                await socket.CloseOutputAsync(WebSocketCloseStatus.NormalClosure, null, timeout.Token)
                    .ConfigureAwait(false);
            }
            catch (Exception err) when (err is WebSocketException or ObjectDisposedException
                                            or OperationCanceledException or InvalidOperationException)
            {
                socket.Abort();
            }
        }
    }

    public async ValueTask DisposeAsync()
    {
        if (Interlocked.Exchange(ref _disposed, 1) == 1)
        {
            return;
        }

        await CloseAsync().ConfigureAwait(false);
        socket.Dispose();
        _sendLock.Dispose();
    }
}

/// <summary>The errors a gateway stream that closed early stands for.</summary>
internal static partial class StreamErrors
{
    [GeneratedRegex("session not found|command not found in the session|the session has ended",
        RegexOptions.IgnoreCase)]
    private static partial Regex SessionGone();

    /// <summary>The error a stream that closed without an exit code stands for.</summary>
    public static MogeniusException FromClose(int code, string? reason, string streamUrl)
    {
        reason ??= "";
        if (reason == "POD_DOES_NOT_EXIST")
        {
            return new MogeniusNotFoundException("The sandbox pod does not exist (any more).",
                source: ErrorSource.Operator);
        }

        if (reason == "UNAUTHORIZED")
        {
            // the key may not open this stream: a viewer, or no grant on the cluster
            return new MogeniusForbiddenException(
                "The stream gateway refused the key for this stream (UNAUTHORIZED).", source: ErrorSource.Api);
        }

        if (reason == "OPERATOR_NOT_CONNECTED")
        {
            return new MogeniusTimeoutException(
                "The cluster operator did not connect to the stream gateway. It dials the address the platform gives it (MO_PRODUCT_NEST__K8S_CMD_STREAM_WEBSOCKET_HOST), which must be reachable from where the operator runs.",
                errorCode: "OPERATOR_TIMEOUT", source: ErrorSource.Api);
        }

        // the operator refused the request before the stream opened
        if (SessionGone().IsMatch(reason))
        {
            return new MogeniusNotFoundException(reason, source: ErrorSource.Operator);
        }

        if (code == 1006)
        {
            return new MogeniusException(
                $"Could not connect to the stream gateway at {streamUrl}: check MOGENIUS_STREAM_URL, the key, and that OrganizationId and ClusterId are set and allow commands in this pod.",
                source: ErrorSource.Sdk);
        }

        if (code == 1000)
        {
            return new MogeniusException("The stream closed before the command reported an exit code.",
                source: ErrorSource.Api);
        }

        return new MogeniusException(reason.Length > 0 ? reason : $"The stream closed with code {code}.",
            source: ErrorSource.Api);
    }
}
