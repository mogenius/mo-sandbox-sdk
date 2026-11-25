using System.Text.Json;
using static Mogenius.Sandboxes.Tests.Fixtures;

namespace Mogenius.Sandboxes.Tests;

public class ClientTests
{
    [Fact]
    public async Task Create_claims_from_the_default_profile_waits_on_the_platform_and_sends_the_platform_headers()
    {
        var platform = new FakePlatform().Answer(201, SandboxInfo());
        using var mogenius = Client(platform);

        var sandbox = await mogenius.CreateAsync(cancellationToken: TestContext.Current.CancellationToken);

        Assert.Equal("default-abc12", sandbox.Id);
        Assert.Equal(SandboxState.Started, sandbox.State);
        Assert.Equal("default-k27tp", sandbox.PodName);
        var call = Assert.Single(platform.Calls);
        Assert.Equal(HttpMethod.Post, call.Method);
        Assert.Equal("https://api.test/sandbox/agent-sandbox", call.Url.ToString());
        Assert.Equal("Bearer mo_pat:user:secret", call.Headers["authorization"]);
        Assert.Equal("org-1", call.Headers["organization-id"]);
        Assert.Equal("cluster-1", call.Headers["cluster-id"]);
        Assert.StartsWith("application/json", call.ContentType);
        Assert.Equal("""{"profile":"default","waitForStart":true,"waitTimeoutSeconds":60}""", call.BodyText);
    }

    [Fact]
    public async Task Create_claims_from_the_configured_profile_unless_the_params_name_one()
    {
        var platform = new FakePlatform().Answer(201, SandboxInfo(("state", "creating")));
        using var mogenius = Client(platform, configure: o => o.Profile = "big");
        var ct = TestContext.Current.CancellationToken;

        await mogenius.CreateAsync(new CreateSandboxParams(), TimeSpan.Zero, ct);
        await mogenius.CreateAsync(new CreateSandboxParams { Snapshot = "gpu" }, TimeSpan.Zero, ct);
        await mogenius.CreateAsync(new CreateSandboxParams { Image = "python:3.13" }, TimeSpan.Zero, ct);

        Assert.Equal(["big", "gpu", "big"], platform.Calls.Select(c => c.Json.GetProperty("profile").GetString()));
    }

    [Fact]
    public async Task Create_maps_params_onto_the_platform_request()
    {
        var platform = new FakePlatform().Answer(201, SandboxInfo(("kind", "Sandbox"), ("id", "custom")));
        using var mogenius = Client(platform, configure: o => o.Namespace = "team-a");

        await mogenius.CreateAsync(new CreateSandboxParams
        {
            Name = "custom",
            Snapshot = "gpu",
            Image = "python:3.13",
            Labels = new Dictionary<string, string> { ["team"] = "a" },
            EnvVars = new Dictionary<string, string> { ["FOO"] = "bar" },
            AutoDeleteInterval = 30,
            AutoStopInterval = 20,
            Ephemeral = false,
            Language = CodeLanguage.TypeScript,
        }, TimeSpan.FromSeconds(120), TestContext.Current.CancellationToken);

        Assert.Equal("/sandbox/team-a", platform.Calls[0].Url.AbsolutePath);
        Assert.Equal(
            """{"name":"custom","profile":"gpu","image":"python:3.13","labels":{"team":"a"},"env":{"FOO":"bar"},"ttlMinutes":30,"autoStopInterval":20,"ephemeral":false,"waitForStart":true,"waitTimeoutSeconds":120}""",
            platform.Calls[0].BodyText);
    }

    [Fact]
    public async Task Create_does_not_wait_with_timeout_zero()
    {
        var platform = new FakePlatform().Answer(201, SandboxInfo(("state", "creating"), ("podName", null)));
        using var mogenius = Client(platform);

        var sandbox = await mogenius.CreateAsync(timeout: TimeSpan.Zero,
            cancellationToken: TestContext.Current.CancellationToken);

        Assert.Equal(SandboxState.Creating, sandbox.State);
        Assert.Single(platform.Calls);
        Assert.False(platform.Calls[0].Json.GetProperty("waitForStart").GetBoolean());
        Assert.False(platform.Calls[0].Json.TryGetProperty("waitTimeoutSeconds", out _));
    }

    [Fact]
    public async Task WaitUntilStarted_polls_until_the_sandbox_is_started()
    {
        var platform = new FakePlatform()
            .Answer(201, SandboxInfo(("state", "starting")))
            .Answer(body: SandboxInfo(("state", "starting")))
            .Answer(body: SandboxInfo());
        using var mogenius = Client(platform);
        var sandbox = await mogenius.CreateAsync(timeout: TimeSpan.Zero,
            cancellationToken: TestContext.Current.CancellationToken);

        await sandbox.WaitUntilStartedAsync(TimeSpan.FromSeconds(10), TestContext.Current.CancellationToken);

        Assert.Equal(SandboxState.Started, sandbox.State);
        Assert.Equal(3, platform.Calls.Count);
    }

    [Fact]
    public async Task WaitUntilStarted_throws_on_error_state_and_on_timeout()
    {
        var platform = new FakePlatform().Answer(201,
            SandboxInfo(("state", "error"), ("stateReason", "ImagePullBackOff")));
        using var mogenius = Client(platform);
        var sandbox = await mogenius.CreateAsync(timeout: TimeSpan.Zero,
            cancellationToken: TestContext.Current.CancellationToken);

        var error = await Assert.ThrowsAsync<MogeniusConflictException>(() => sandbox.WaitUntilStartedAsync());
        Assert.Equal("SANDBOX_ERROR", error.ErrorCode);
        Assert.Contains("ImagePullBackOff", error.Message);

        var slow = new FakePlatform().Answer(201, SandboxInfo(("state", "starting")));
        using var other = Client(slow);
        var starting = await other.CreateAsync(timeout: TimeSpan.Zero,
            cancellationToken: TestContext.Current.CancellationToken);
        await Assert.ThrowsAsync<MogeniusTimeoutException>(() =>
            starting.WaitUntilStartedAsync(TimeSpan.FromMilliseconds(50)));
    }

    [Fact]
    public async Task List_filters_by_labels_and_state_and_pages()
    {
        var platform = new FakePlatform().Answer(body: new
        {
            items = new[] { SandboxInfo() },
            nextCursor = "next",
            totalCount = 3,
        });
        using var mogenius = Client(platform);

        var page = await mogenius.ListAsync(new Dictionary<string, string> { ["team"] = "a", ["env"] = "dev" }, 1,
            "abc", SandboxState.Stopped, TestContext.Current.CancellationToken);

        Assert.Single(page.Items);
        Assert.Equal("next", page.NextCursor);
        Assert.Equal(3, page.Total);
        var call = platform.Calls[0];
        Assert.Equal("team=a,env=dev", call.Query("labels"));
        Assert.Equal("stopped", call.Query("state"));
        Assert.Equal("1", call.Query("limit"));
        Assert.Equal("abc", call.Query("cursor"));
    }

    [Fact]
    public async Task FindOne_throws_not_found_on_an_empty_page()
    {
        var platform = new FakePlatform().Answer(body: new { items = Array.Empty<object>(), nextCursor = (string?)null, totalCount = 0 });
        using var mogenius = Client(platform);

        var error = await Assert.ThrowsAsync<MogeniusNotFoundException>(() =>
            mogenius.FindOneAsync(new SandboxFilter(Labels: new Dictionary<string, string> { ["x"] = "y" })));
        Assert.Equal("SANDBOX_NOT_FOUND", error.ErrorCode);
    }

    [Fact]
    public async Task WarmPools_reads_and_sorts_the_pools()
    {
        var platform = new FakePlatform().Answer(body: new
        {
            status = "success",
            data = new
            {
                items = new object[]
                {
                    new
                    {
                        metadata = new { name = "z", @namespace = "b" },
                        spec = new { replicas = 2, sandboxTemplateRef = new { name = "tz" } },
                        status = new { readyReplicas = 1 },
                    },
                    new
                    {
                        metadata = new { name = "a", @namespace = "b" },
                        spec = new { replicas = 3, sandboxTemplateRef = new { name = "ta" } },
                    },
                    new { metadata = new { name = "m", @namespace = "a" } },
                },
            },
        });
        using var mogenius = Client(platform);

        var pools = await mogenius.WarmPoolsAsync(cancellationToken: TestContext.Current.CancellationToken);

        Assert.Equal(
        [
            new WarmPool("m", "a", "", 0, 0),
            new WarmPool("a", "b", "ta", 0, 3),
            new WarmPool("z", "b", "tz", 1, 2),
        ], pools);
        Assert.Equal("agent-sandbox", platform.Calls[0].Query("namespace"));
        Assert.Equal("SandboxWarmPool", platform.Calls[0].Query("kind"));
    }

    [Fact]
    public async Task WarmPools_is_empty_without_the_resource_type()
    {
        var platform = new FakePlatform()
            .Answer(body: new { status = "error", statusCode = 500, message = "no matches for kind SandboxWarmPool" })
            .Answer(404, new { message = "nope" });
        using var mogenius = Client(platform);

        Assert.Empty(await mogenius.WarmPoolsAsync(true, TestContext.Current.CancellationToken));
        Assert.Empty(await mogenius.WarmPoolsAsync(true, TestContext.Current.CancellationToken));
        Assert.Null(platform.Calls[0].Query("namespace"));
    }

    [Fact]
    public async Task Lifecycle_calls_hit_the_sandbox_routes()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: SandboxInfo(("state", "stopped")))
            .Answer(body: SandboxInfo(("labels", new Dictionary<string, string> { ["a"] = "b" })))
            .Answer(body: SandboxInfo());
        var ct = TestContext.Current.CancellationToken;

        await sandbox.StopAsync(cancellationToken: ct);
        var labels = await sandbox.SetLabelsAsync(new Dictionary<string, string> { ["a"] = "b" }, ct);
        await sandbox.SetAutoDeleteIntervalAsync(12.7, ct);

        Assert.Equal("b", labels["a"]);
        Assert.Equal("/sandbox/agent-sandbox/default-abc12/stop", platform.Calls[0].Url.AbsolutePath);
        Assert.Equal(HttpMethod.Patch, platform.Calls[1].Method);
        Assert.Equal("""{"labels":{"a":"b"}}""", platform.Calls[1].BodyText);
        Assert.Equal("""{"ttlMinutes":12}""", platform.Calls[2].BodyText);
    }

    [Fact]
    public async Task Unsupported_features_throw()
    {
        var sandbox = await Sandbox(new FakePlatform());
        using var mogenius = Client(new FakePlatform());

        var error = await Assert.ThrowsAsync<MogeniusUnsupportedException>(sandbox.ForkAsync);
        Assert.Equal("UNSUPPORTED", error.ErrorCode);
        await Assert.ThrowsAsync<MogeniusUnsupportedException>(sandbox.ArchiveAsync);
        Assert.Throws<MogeniusUnsupportedException>(() => mogenius.Snapshot);
    }

    [Fact]
    public async Task A_sandbox_without_a_pod_is_read_again_before_giving_up()
    {
        var platform = new FakePlatform()
            .Answer(201, SandboxInfo(("state", "creating"), ("podName", null)))
            .Answer(body: SandboxInfo(("state", "creating"), ("podName", null)));
        using var mogenius = Client(platform);
        var sandbox = await mogenius.CreateAsync(timeout: TimeSpan.Zero,
            cancellationToken: TestContext.Current.CancellationToken);

        await Assert.ThrowsAsync<MogeniusConflictException>(() => sandbox.Process.ExecuteCommandAsync("ls"));
        Assert.Equal(HttpMethod.Get, platform.Calls[1].Method);
    }

    [Fact]
    public void Options_need_an_api_key()
    {
        if (!string.IsNullOrEmpty(Environment.GetEnvironmentVariable("MOGENIUS_API_KEY")))
        {
            return;
        }

        var error = Assert.Throws<MogeniusException>(() => new MogeniusClient(new MogeniusOptions()));
        Assert.Contains("No API key", error.Message);
    }

    [Fact]
    public async Task Platform_errors_become_typed_exceptions()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(404, new { statusCode = 404, errorCode = "SANDBOX_NOT_FOUND", source = "operator", message = new[] { "a", "b" } });

        var error = await Assert.ThrowsAsync<MogeniusNotFoundException>(() => sandbox.RefreshDataAsync());

        Assert.Equal("a, b", error.Message);
        Assert.Equal(404, error.StatusCode);
        Assert.Equal(ErrorSource.Operator, error.ErrorSource);
    }

    [Theory]
    [InlineData(400, null, typeof(MogeniusValidationException))]
    [InlineData(401, null, typeof(MogeniusUnauthorizedException))]
    [InlineData(403, null, typeof(MogeniusForbiddenException))]
    [InlineData(408, null, typeof(MogeniusProcessExecutionTimeoutException))]
    [InlineData(409, null, typeof(MogeniusConflictException))]
    [InlineData(424, null, typeof(MogeniusOperatorUpgradeRequiredException))]
    [InlineData(504, null, typeof(MogeniusTimeoutException))]
    [InlineData(500, "EXEC_TIMEOUT", typeof(MogeniusProcessExecutionTimeoutException))]
    [InlineData(500, "SESSION_BUSY", typeof(MogeniusConflictException))]
    [InlineData(500, "FILE_PERMISSION_DENIED", typeof(MogeniusForbiddenException))]
    [InlineData(500, null, typeof(MogeniusException))]
    public void Errors_map_by_code_first_and_status_second(int status, string? code, Type expected)
    {
        var body = JsonSerializer.Serialize(new { errorCode = code, message = "boom" });

        var error = MogeniusErrors.FromResponse(status, body, "fallback");

        Assert.Equal(expected, error.GetType());
        Assert.Equal("boom", error.Message);
        Assert.Equal("plain text", MogeniusErrors.FromResponse(500, "plain text", "fallback").Message);
        Assert.Equal("fallback", MogeniusErrors.FromResponse(500, null, "fallback").Message);
    }
}
