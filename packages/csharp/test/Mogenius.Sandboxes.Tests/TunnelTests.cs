using System.Net.Sockets;
using System.Text;
using static Mogenius.Sandboxes.Tests.Fixtures;

namespace Mogenius.Sandboxes.Tests;

public class TunnelTests
{
    private static async Task<(Tunnel Tunnel, FakeWebSocket Socket)> OpenAsync(Sandbox sandbox,
        List<FakeWebSocket> sockets, int port = 8080)
    {
        var opening = sandbox.TunnelAsync(port, cancellationToken: TestContext.Current.CancellationToken);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        socket.Frame("PEER_IS_READY");
        return (await opening, socket);
    }

    [Fact]
    public async Task A_tunnel_waits_for_the_operator_and_listens_locally()
    {
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(new FakePlatform(), sockets);

        var (tunnel, socket) = await OpenAsync(sandbox, sockets);
        await using var _ = tunnel;

        Assert.Equal("PORT_FORWARD", socket.Query("type"));
        Assert.Equal("Pod", socket.Query("kind"));
        Assert.Equal("default-k27tp", socket.Query("workloadName"));
        Assert.Equal("8080", socket.Query("remotePort"));
        Assert.Equal("PEER_IS_READY", socket.SentText.First());
        Assert.True(tunnel.Port > 0);
        Assert.Equal($"http://127.0.0.1:{tunnel.Port}", tunnel.Url);
    }

    [Fact]
    public async Task Bytes_travel_both_ways_behind_the_connection_id()
    {
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(new FakePlatform(), sockets);
        var (tunnel, socket) = await OpenAsync(sandbox, sockets);
        await using var _ = tunnel;
        var ct = TestContext.Current.CancellationToken;

        using var client = new TcpClient();
        await client.ConnectAsync("127.0.0.1", tunnel.Port, ct);
        var stream = client.GetStream();
        await Eventually(() => socket.SentText.FirstOrDefault(t => t == "PFM:O:c1"));
        await stream.WriteAsync("ping"u8.ToArray(), ct);
        var frame = await Eventually(() => socket.SentBinary.FirstOrDefault());
        Assert.Equal([2, (byte)'c', (byte)'1', .. "ping"u8.ToArray()], frame);

        socket.Frame([2, (byte)'c', (byte)'1', .. "pong"u8.ToArray()]);
        var buffer = new byte[4];
        await stream.ReadExactlyAsync(buffer, ct);
        Assert.Equal("pong", Encoding.UTF8.GetString(buffer));

        socket.Frame("PFM:C:c1");
        Assert.Equal(0, await stream.ReadAsync(buffer, ct));
    }

    [Fact]
    public async Task CreateConnection_bridges_an_in_process_stream()
    {
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(new FakePlatform(), sockets);
        var (tunnel, socket) = await OpenAsync(sandbox, sockets);
        var ct = TestContext.Current.CancellationToken;

        var connection = tunnel.CreateConnection();
        await Eventually(() => socket.SentText.FirstOrDefault(t => t == "PFM:O:c1"));
        await connection.WriteAsync("hi"u8.ToArray(), ct);
        await Eventually(() => socket.SentBinary.FirstOrDefault());
        socket.Frame([2, (byte)'c', (byte)'1', (byte)'!']);
        var buffer = new byte[1];
        await connection.ReadExactlyAsync(buffer, ct);
        Assert.Equal((byte)'!', buffer[0]);

        await connection.DisposeAsync();
        await Eventually(() => socket.SentText.FirstOrDefault(t => t == "PFM:C:c1"));

        await tunnel.CloseAsync();
        Assert.Null(await tunnel.Completion);
        Assert.Throws<MogeniusException>(() => tunnel.CreateConnection());
    }

    [Fact]
    public async Task A_missing_pod_fails_the_open()
    {
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(new FakePlatform(), sockets);

        var opening = sandbox.TunnelAsync(80, cancellationToken: TestContext.Current.CancellationToken);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        socket.Frame("POD_DOES_NOT_EXIST");

        await Assert.ThrowsAsync<MogeniusNotFoundException>(() => opening);
    }

    [Fact]
    public async Task Ports_outside_the_tcp_range_are_rejected()
    {
        var sandbox = await Sandbox(new FakePlatform());

        await Assert.ThrowsAsync<MogeniusValidationException>(() => sandbox.TunnelAsync(70000));
    }
}
