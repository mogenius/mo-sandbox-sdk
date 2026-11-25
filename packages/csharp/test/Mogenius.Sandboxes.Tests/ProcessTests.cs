using System.Text;
using static Mogenius.Sandboxes.Tests.Fixtures;

namespace Mogenius.Sandboxes.Tests;

public class ProcessTests
{
    private static object ExecAnswer(int exitCode = 0, string stdout = "", string stderr = "") => new
    {
        exitCode,
        result = stdout + stderr,
        artifacts = new { stdout, stderr },
        truncated = false,
        container = "sandbox",
        durationMs = 5,
    };

    [Fact]
    public async Task ExecuteCommand_posts_to_the_pod_route_in_the_sandbox_container()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: ExecAnswer(0, "hi\n", "warn\n"));

        var result = await sandbox.Process.ExecuteCommandAsync("echo hi", "/work",
            new Dictionary<string, string> { ["A"] = "1" }, TimeSpan.FromSeconds(2.5),
            cancellationToken: TestContext.Current.CancellationToken);

        Assert.Equal(0, result.ExitCode);
        Assert.Equal("hi\nwarn\n", result.Result);
        Assert.Equal("hi\n", result.Artifacts!.Stdout);
        Assert.Equal("warn\n", result.Artifacts.Stderr);
        var call = platform.Calls[0];
        Assert.Equal("/resource/exec/agent-sandbox/default-k27tp", call.Url.AbsolutePath);
        Assert.Equal("""{"command":"echo hi","cwd":"/work","env":{"A":"1"},"timeout":3,"container":"sandbox"}""",
            call.BodyText);
    }

    [Fact]
    public async Task ExecuteCommand_defaults_to_ten_seconds_and_maps_a_timeout()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: ExecAnswer()).Answer(408, new { errorCode = "EXEC_TIMEOUT", message = "too slow" });

        await sandbox.Process.ExecuteCommandAsync("true", cancellationToken: TestContext.Current.CancellationToken);
        await Assert.ThrowsAsync<MogeniusProcessExecutionTimeoutException>(() =>
            sandbox.Process.ExecuteCommandAsync("sleep 100"));

        Assert.Equal(10, platform.Calls[0].Json.GetProperty("timeout").GetInt32());
    }

    [Fact]
    public async Task CodeRun_sends_the_snippet_base64_encoded_and_extracts_charts()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        var stdout = "before\n__mo_chart__:{\"type\":\"bar\",\"title\":\"T\",\"png\":\"iVBOR\"}\n__mo_chart__:not json\nafter";
        platform.Answer(body: ExecAnswer(0, stdout, "err\n"));

        var result = await sandbox.Process.CodeRunAsync("print('it''s')",
            new CodeRunParams { Argv = ["a b", "it's"] }, cancellationToken: TestContext.Current.CancellationToken);

        var command = platform.Calls[0].Json.GetProperty("command").GetString()!;
        Assert.Contains(Convert.ToBase64String(Encoding.UTF8.GetBytes("print('it''s')")), command);
        Assert.Contains("python3 -c \"$(printf %s '", command);
        Assert.Contains(" 'a b' 'it'\\''s'; __mo_rc=$?", command);
        var chart = Assert.Single(result.Artifacts!.Charts!);
        Assert.Equal("bar", chart.Type);
        Assert.Equal("T", chart.Title);
        Assert.Equal("before\n__mo_chart__:not json\nafter", result.Artifacts.Stdout);
        Assert.Equal("before\n__mo_chart__:not json\naftererr\n", result.Result);
    }

    [Fact]
    public void CodeRun_command_runs_node_and_tsx_without_the_bootstrap()
    {
        var js = ProcessService.BuildCodeRunCommand("console.log(1)", CodeLanguage.JavaScript, []);
        var ts = ProcessService.BuildCodeRunCommand("let a = 1", CodeLanguage.TypeScript, []);

        Assert.Equal(
            "__mo_f=/tmp/mo_code_$$.js; printf %s 'Y29uc29sZS5sb2coMSk=' | base64 -d > \"$__mo_f\" && node \"$__mo_f\"; __mo_rc=$?; rm -f \"$__mo_f\"; exit $__mo_rc",
            js);
        Assert.Contains("&& tsx \"$__mo_f\";", ts);
    }

    [Fact]
    public void The_python_bootstrap_is_the_one_the_other_sdks_carry()
    {
        var dir = new DirectoryInfo(AppContext.BaseDirectory);
        while (dir is not null && !Directory.Exists(Path.Combine(dir.FullName, "packages", "go")))
        {
            dir = dir.Parent;
        }

        Assert.NotNull(dir);
        var go = File.ReadAllText(Path.Combine(dir.FullName, "packages", "go", "pkg", "mogenius", "code_run.py"));
        Assert.Equal(go, PythonBootstrap.Source);
    }

    [Fact]
    public async Task ExecuteCommandStream_sends_the_request_when_asked_and_yields_output_and_exit()
    {
        var platform = new FakePlatform();
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(platform, sockets);
        var ct = TestContext.Current.CancellationToken;

        var events = new List<ExecEvent>();
        var reading = Task.Run(async () =>
        {
            await foreach (var item in sandbox.Process.ExecuteCommandStreamAsync("make", timeout: TimeSpan.FromSeconds(30),
                               cancellationToken: ct))
            {
                events.Add(item);
            }
        }, ct);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        socket.Frame("PEER_IS_READY");
        socket.Frame("SEND_EXEC_REQUEST");
        socket.Frame(StreamBytes(0, "out"));
        socket.Frame(StreamBytes(1, "err"));
        socket.Frame("TRUNCATED");
        socket.Frame("EXIT:2");
        socket.End(1000);
        await reading;

        Assert.Equal("CLUSTER__POD_EXEC", socket.Query("type"));
        Assert.Equal("bearer mo_pat:user:secret", socket.Query("authorization"));
        Assert.Equal("org-1", socket.Query("organizationId"));
        Assert.Equal("cluster-1", socket.Query("clusterId"));
        Assert.Equal("default-k27tp", socket.Query("podName"));
        Assert.Equal("sandbox", socket.Query("container"));
        Assert.Equal("/xterm-stream", socket.Url.AbsolutePath);
        Assert.Equal("""{"command":"make","timeout":30}""", Assert.Single(socket.SentText));
        Assert.Equal(3, events.Count);
        Assert.Equal("out", ((ExecOutputEvent)events[0]).Text);
        Assert.Equal(OutputStream.Stderr, ((ExecOutputEvent)events[1]).Stream);
        Assert.Equal(new ExecExitEvent(2, true, false), events[2]);
    }

    [Fact]
    public async Task ExecuteCommandStream_maps_a_close_without_exit_code()
    {
        var platform = new FakePlatform();
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(platform, sockets);
        var ct = TestContext.Current.CancellationToken;

        var reading = Task.Run(async () =>
        {
            await foreach (var _ in sandbox.Process.ExecuteCommandStreamAsync("ls", cancellationToken: ct))
            {
            }
        }, ct);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        socket.End(4000, "POD_DOES_NOT_EXIST");

        await Assert.ThrowsAsync<MogeniusNotFoundException>(() => reading);
    }

    [Fact]
    public async Task ExecuteCommandStream_closes_the_stream_when_the_loop_is_left()
    {
        var platform = new FakePlatform();
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(platform, sockets);
        var ct = TestContext.Current.CancellationToken;

        var reading = Task.Run(async () =>
        {
            await foreach (var _ in sandbox.Process.ExecuteCommandStreamAsync("yes", cancellationToken: ct))
            {
                break;
            }
        }, ct);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        socket.Frame(StreamBytes(0, "y"));
        await reading;

        Assert.Equal(1000, socket.ClosedWith);
    }

    [Fact]
    public async Task Sessions_use_the_pod_routes()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        var ct = TestContext.Current.CancellationToken;
        platform
            .Answer(201, new { sessionId = "s1", container = "sandbox", createdAt = "t", lastUsedAt = "t", commands = Array.Empty<object>() })
            .Answer(body: new { cmdId = "c1", exitCode = (int?)null })
            .Answer(body: new { id = "c1", command = "sleep 1", exitCode = 0, truncated = true })
            .Answer(body: new { output = "done\n" });

        var session = await sandbox.Process.CreateSessionAsync("s1",
            new CreateSessionOptions { Env = new Dictionary<string, string> { ["K"] = "v" } }, ct);
        var exec = await sandbox.Process.ExecuteSessionCommandAsync("s1",
            new SessionExecuteRequest { Command = "sleep 1", RunAsync = true, Timeout = TimeSpan.FromSeconds(5) }, ct);
        var command = await sandbox.Process.GetSessionCommandAsync("s1", "c1", ct);
        var logs = await sandbox.Process.GetSessionCommandLogsAsync("s1", "c1", ct);

        Assert.Equal("s1", session.SessionId);
        Assert.Equal("/resource/session/agent-sandbox/default-k27tp", platform.Calls[0].Url.AbsolutePath);
        Assert.Equal("""{"sessionId":"s1","container":"sandbox","env":{"K":"v"}}""", platform.Calls[0].BodyText);
        Assert.Equal(new SessionExecuteResponse("c1"), exec);
        Assert.Equal("/resource/session/agent-sandbox/default-k27tp/s1/exec", platform.Calls[1].Url.AbsolutePath);
        Assert.Equal("""{"command":"sleep 1","runAsync":true,"timeout":5}""", platform.Calls[1].BodyText);
        Assert.Equal(new SessionCommand("c1", "sleep 1", 0, true), command);
        Assert.Equal("done\n", logs);
        Assert.Equal("/resource/session/agent-sandbox/default-k27tp/s1/command/c1/logs",
            platform.Calls[3].Url.AbsolutePath);
    }

    [Fact]
    public async Task Session_logs_follow_the_stream_and_join_split_characters()
    {
        var platform = new FakePlatform();
        var sockets = new List<FakeWebSocket>();
        var sandbox = await Sandbox(platform, sockets);
        var chunks = new List<string>();

        var following = sandbox.Process.GetSessionCommandLogsAsync("s1", "c1", chunks.Add,
            TestContext.Current.CancellationToken);
        var socket = await Eventually(() => sockets.FirstOrDefault());
        var euro = Encoding.UTF8.GetBytes("€");
        socket.Frame([0, (byte)'a', euro[0]]);
        socket.Frame([0, euro[1], euro[2]]);
        socket.Frame("EXIT:0");
        socket.End(1000);
        await following;

        Assert.Equal("CLUSTER__POD_SESSION_LOG", socket.Query("type"));
        Assert.Equal("c1", socket.Query("cmdId"));
        Assert.Equal("a€", string.Concat(chunks));
        Assert.Empty(socket.SentText);
    }
}
