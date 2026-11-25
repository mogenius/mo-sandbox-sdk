using System.Net;
using System.Net.Http.Headers;
using System.Text;
using static Mogenius.Sandboxes.Tests.Fixtures;

namespace Mogenius.Sandboxes.Tests;

public class FileSystemTests
{
    private static object FileEntry(string name) => new
    {
        name,
        path = $"/home/{name}",
        isDir = false,
        size = 3,
        modTime = "2026-10-01T12:00:00Z",
        mode = "0644",
        permissions = "-rw-r--r--",
        owner = "u",
        group = "g",
        mimeType = (string?)null,
    };

    [Fact]
    public async Task ListFiles_addresses_the_sandbox_container_and_maps_entries()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: new[] { FileEntry("a.txt") });

        var files = await sandbox.Fs.ListFilesAsync("/home", TestContext.Current.CancellationToken);

        var file = Assert.Single(files);
        Assert.Equal("/home/a.txt", file.Path);
        Assert.Null(file.MimeType);
        var call = platform.Calls[0];
        Assert.Equal("/resource/files/agent-sandbox/default-k27tp", call.Url.AbsolutePath);
        Assert.Equal("sandbox", call.Query("container"));
        Assert.Equal("/home", call.Query("path"));
    }

    [Fact]
    public async Task File_operations_hit_their_routes()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        var ct = TestContext.Current.CancellationToken;
        platform.Answer(201)
            .Answer(201)
            .Answer(200)
            .Answer(body: FileEntry("x"))
            .Answer(body: new { files = new[] { "/a.py" }, truncated = true })
            .Answer(body: new { matches = new[] { new { file = "/a.py", line = 3, content = "x" } }, truncated = false })
            .Answer(body: new[] { new { file = "/a.py", success = true, error = (string?)null } });

        await sandbox.Fs.CreateFolderAsync("/d", "755", ct);
        await sandbox.Fs.MoveFilesAsync("/a", "/b", ct);
        await sandbox.Fs.DeleteFileAsync("/d", true, ct);
        await sandbox.Fs.SetFilePermissionsAsync("/x", new SetFilePermissionsParams(Mode: "u+x"), ct);
        var search = await sandbox.Fs.SearchFilesAsync("/", "*.py", ct);
        var matches = await sandbox.Fs.FindFilesAsync("/", "x", ct);
        var replaced = await sandbox.Fs.ReplaceInFilesAsync(["/a.py"], "x", "y", ct);

        Assert.Equal("/resource/files/agent-sandbox/default-k27tp/folder", platform.Calls[0].Url.AbsolutePath);
        Assert.Equal("755", platform.Calls[0].Query("mode"));
        Assert.Equal("/b", platform.Calls[1].Query("destination"));
        Assert.Equal(HttpMethod.Delete, platform.Calls[2].Method);
        Assert.Equal("true", platform.Calls[2].Query("recursive"));
        Assert.Equal("u+x", platform.Calls[3].Query("mode"));
        Assert.Null(platform.Calls[3].Query("owner"));
        Assert.True(search.Truncated);
        Assert.Equal(new FileMatch("/a.py", 3, "x"), Assert.Single(matches));
        Assert.True(Assert.Single(replaced).Success);
        Assert.Equal("""{"files":["/a.py"],"pattern":"x","newValue":"y"}""", platform.Calls[6].BodyText);
    }

    [Fact]
    public async Task UploadFile_posts_a_multipart_form_with_the_file_name()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: new[] { new { path = "/home/a.txt", success = true, error = (string?)null } });

        await sandbox.Fs.UploadFileAsync("hello", "/home/a.txt", cancellationToken: TestContext.Current.CancellationToken);

        var call = platform.Calls[0];
        Assert.Equal("/resource/files/agent-sandbox/default-k27tp/upload", call.Url.AbsolutePath);
        Assert.Equal("/home/a.txt", call.Query("path"));
        Assert.Equal("sandbox", call.Query("container"));
        Assert.StartsWith("multipart/form-data", call.ContentType);
        Assert.Contains("name=\"file\"; filename=\"a.txt\"", call.BodyText);
        Assert.Contains("\r\n\r\nhello\r\n", call.BodyText);
    }

    [Fact]
    public async Task UploadFile_throws_when_the_operator_reports_a_failure()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: new[] { new { path = "/ro/a", success = false, error = "read-only file system" } });

        var error = await Assert.ThrowsAsync<MogeniusException>(() =>
            sandbox.Fs.UploadFileAsync(new byte[] { 1 }, "/ro/a"));

        Assert.Contains("read-only file system", error.Message);
    }

    [Fact]
    public async Task UploadFileStream_streams_the_source_and_reports_progress()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: new[] { new { path = "/big", success = true, error = (string?)null } });
        var sent = new List<long>();

        await sandbox.Fs.UploadFileStreamAsync(new MemoryStream(Encoding.UTF8.GetBytes("streamed")), "/big",
            new SyncProgress<UploadProgress>(p => sent.Add(p.BytesSent)),
            cancellationToken: TestContext.Current.CancellationToken);

        Assert.Contains("boundary=\"----mogenius-", platform.Calls[0].ContentType);
        Assert.Contains("filename=\"big\"", platform.Calls[0].BodyText);
        Assert.Contains("streamed", platform.Calls[0].BodyText);
        Assert.Equal(8, sent.Last());
    }

    [Fact]
    public async Task UploadFiles_names_each_part_after_its_destination()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(body: new object[]
        {
            new { path = "/a/1.txt", success = true, error = (string?)null },
            new { path = "/b/2.txt", success = false, error = "nope" },
        });

        var results = await sandbox.Fs.UploadFilesAsync(
            [FileUpload.FromText("1", "/a/1.txt"), FileUpload.FromText("2", "/b/2.txt")],
            cancellationToken: TestContext.Current.CancellationToken);

        Assert.Equal(2, results.Count);
        Assert.False(results[1].Success);
        Assert.Contains("name=\"/a/1.txt\"; filename=\"1.txt\"", platform.Calls[0].BodyText);
        Assert.Contains("name=\"/b/2.txt\"; filename=\"2.txt\"", platform.Calls[0].BodyText);
        Assert.Null(platform.Calls[0].Query("path"));
    }

    [Fact]
    public async Task DownloadFile_fetches_the_link_without_auth_headers()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(201, new { url = "https://dl.test/once?token=t", expiresInSeconds = 60 })
            .Answer(_ => new Answer(Response: () => new HttpResponseMessage(HttpStatusCode.OK)
            {
                Content = new ByteArrayContent(Encoding.UTF8.GetBytes("content")),
            }));

        var bytes = await sandbox.Fs.DownloadFileAsync("/f", TestContext.Current.CancellationToken);

        Assert.Equal("content", Encoding.UTF8.GetString(bytes));
        Assert.Equal("/resource/files/agent-sandbox/default-k27tp/download-link",
            platform.Calls[0].Url.AbsolutePath);
        Assert.Equal("https://dl.test/once?token=t", platform.Calls[1].Url.ToString());
        Assert.False(platform.Calls[1].Headers.ContainsKey("authorization"));
    }

    [Fact]
    public async Task DownloadFileStream_resumes_with_a_range_request_after_a_drop()
    {
        FileSystemService.ResumeDelays = [TimeSpan.Zero, TimeSpan.Zero];
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        var content = Encoding.UTF8.GetBytes("0123456789");
        platform.Answer(201, new { url = "https://dl.test/once", expiresInSeconds = 60 })
            .Answer(_ => new Answer(Response: () =>
            {
                var response = new HttpResponseMessage(HttpStatusCode.OK)
                {
                    Content = new StreamContent(new DroppingStream(content[..4])),
                };
                response.Content.Headers.ContentLength = 10;
                response.Headers.ETag = new EntityTagHeaderValue("\"v1\"");
                response.Headers.AcceptRanges.Add("bytes");
                return response;
            }))
            .Answer(_ => new Answer(Response: () =>
            {
                var response = new HttpResponseMessage(HttpStatusCode.PartialContent)
                {
                    Content = new ByteArrayContent(content[4..]),
                };
                response.Content.Headers.ContentRange = new ContentRangeHeaderValue(4, 9, 10);
                return response;
            }));
        var progress = new List<DownloadProgress>();

        await using var stream = await sandbox.Fs.DownloadFileStreamAsync("/f",
            new SyncProgress<DownloadProgress>(progress.Add), TestContext.Current.CancellationToken);
        using var copy = new MemoryStream();
        await stream.CopyToAsync(copy, TestContext.Current.CancellationToken);

        Assert.Equal("0123456789", Encoding.UTF8.GetString(copy.ToArray()));
        Assert.Equal("bytes=4-", platform.Calls[2].Headers["range"]);
        Assert.Equal("\"v1\"", platform.Calls[2].Headers["if-range"]);
        Assert.Equal(new DownloadProgress(10, 10), progress.Last());
    }

    [Fact]
    public async Task DownloadFiles_reports_each_failure_apart()
    {
        var platform = new FakePlatform();
        var sandbox = await Sandbox(platform);
        platform.Answer(404, new { errorCode = "FILE_NOT_FOUND", message = "no such file" });

        var results = await sandbox.Fs.DownloadFilesAsync(["/missing"], TestContext.Current.CancellationToken);

        Assert.Equal("no such file", Assert.Single(results).Error);
    }

    [Fact]
    public void Paths_are_normalized_like_the_typescript_sdk()
    {
        Assert.Equal("/a/c", SftpUploads.Normalize("/a/./b/../c//"));
        Assert.Equal("/", SftpUploads.ParentOf("/a"));
        Assert.Equal("/a", SftpUploads.ParentOf("/a/b"));
        Assert.Equal("b", FileSystemService.Basename("/a/b/"));
    }

    /// <summary>Hands out some bytes, then fails like a dropped connection.</summary>
    private sealed class DroppingStream(byte[] head) : MemoryStream(head)
    {
        public override int Read(byte[] buffer, int offset, int count)
        {
            var read = base.Read(buffer, offset, count);
            return read > 0 ? read : throw new IOException("connection reset");
        }

        public override ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
        {
            var read = base.Read(buffer.Span);
            return read > 0 ? ValueTask.FromResult(read) : throw new IOException("connection reset");
        }
    }
}

/// <summary>An <see cref="IProgress{T}"/> that reports on the caller's thread.</summary>
public sealed class SyncProgress<T>(Action<T> report) : IProgress<T>
{
    public void Report(T value) => report(value);
}
