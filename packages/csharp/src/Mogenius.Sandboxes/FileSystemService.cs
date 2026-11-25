using System.Net;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;

namespace Mogenius.Sandboxes;

/// <summary>
/// The file system of a sandbox. Paths are absolute inside the container or relative to its working directory. Every
/// call goes through the platform API to the operator, which runs the file operation inside the pod; nothing is
/// installed in the image for it.
/// </summary>
public sealed class FileSystemService
{
    /// <summary>Pauses before each resume attempt of a download; tests shorten them.</summary>
    internal static TimeSpan[] ResumeDelays =
    [
        TimeSpan.FromSeconds(1), TimeSpan.FromSeconds(2), TimeSpan.FromSeconds(4), TimeSpan.FromSeconds(8),
        TimeSpan.FromSeconds(16),
    ];

    private readonly ApiClient _api;
    private readonly string _namespace;
    private readonly Func<CancellationToken, Task<PodRef>> _pod;
    private readonly SftpUploads _sftp;

    internal FileSystemService(ApiClient api, string ns, Func<CancellationToken, Task<PodRef>> pod, SftpUploads sftp)
    {
        _api = api;
        _namespace = ns;
        _pod = pod;
        _sftp = sftp;
    }

    /// <summary>Entries directly below <paramref name="path"/> (default: the working directory).</summary>
    public async Task<IReadOnlyList<SandboxFileInfo>> ListFilesAsync(string? path = null,
        CancellationToken cancellationToken = default) =>
        await _api.GetAsync<List<SandboxFileInfo>>(await RouteAsync("", cancellationToken).ConfigureAwait(false),
            new Query { ["path"] = path }, cancellationToken).ConfigureAwait(false) ?? [];

    /// <summary>One file's or folder's details.</summary>
    public async Task<SandboxFileInfo> GetFileDetailsAsync(string path, CancellationToken cancellationToken = default) =>
        await _api.GetAsync<SandboxFileInfo>(await RouteAsync("/info", cancellationToken).ConfigureAwait(false),
            new Query { ["path"] = path }, cancellationToken).ConfigureAwait(false);

    /// <summary>The file's bytes. A folder comes back as a gzipped tar.</summary>
    public async Task<byte[]> DownloadFileAsync(string path, CancellationToken cancellationToken = default)
    {
        await using var stream = await DownloadFileStreamAsync(path, null, cancellationToken).ConfigureAwait(false);
        using var buffer = new MemoryStream();
        await stream.CopyToAsync(buffer, cancellationToken).ConfigureAwait(false);
        return buffer.ToArray();
    }

    /// <summary>
    /// mogenius only: the file as a stream, chunked from the container to the caller without any station holding it
    /// whole — for files too big for memory. The platform hands out a short-lived link that is fetched without auth
    /// headers.
    /// <para>
    /// A file (not a folder) resumes on its own: when the connection drops or ends early, the stream asks the same
    /// link for the rest with a Range request and carries on, up to five times with growing pauses. The bytes the
    /// caller reads stay one uninterrupted, complete file.
    /// </para>
    /// </summary>
    /// <param name="path">the file inside the container</param>
    /// <param name="progress">hears how many bytes were read so far</param>
    /// <param name="cancellationToken">cancels the link request and the first fetch</param>
    public async Task<Stream> DownloadFileStreamAsync(string path, IProgress<DownloadProgress>? progress = null,
        CancellationToken cancellationToken = default)
    {
        var link = await _api.PostAsync<DownloadLinkBody>(
            await RouteAsync("/download-link", cancellationToken).ConfigureAwait(false), null,
            new Query { ["path"] = path }, cancellationToken).ConfigureAwait(false);
        var first = await _api.FetchDownloadAsync(link.Url, ct: cancellationToken).ConfigureAwait(false);
        return await ResumableDownloadStream.CreateAsync(_api, link.Url, first, ResumeDelays, progress,
            cancellationToken).ConfigureAwait(false);
    }

    /// <summary>Downloads several files; a failure for one does not stop the others.</summary>
    public async Task<IReadOnlyList<DownloadFileResult>> DownloadFilesAsync(IEnumerable<string> paths,
        CancellationToken cancellationToken = default) =>
        await Task.WhenAll(paths.Select(async path =>
        {
            try
            {
                return new DownloadFileResult(path,
                    await DownloadFileAsync(path, cancellationToken).ConfigureAwait(false), null);
            }
            catch (Exception err) when (err is not OperationCanceledException)
            {
                return new DownloadFileResult(path, null, err.Message);
            }
        })).ConfigureAwait(false);

    /// <summary>Writes <paramref name="file"/> to <paramref name="path"/>, creating parent folders.</summary>
    /// <param name="file">the content</param>
    /// <param name="path">destination inside the container</param>
    /// <param name="transport">mogenius: HTTP or SFTP for this call; the client's <c>FileTransport</c> is the default</param>
    /// <param name="cancellationToken">cancels the upload</param>
    public async Task UploadFileAsync(ReadOnlyMemory<byte> file, string path, FileTransport? transport = null,
        CancellationToken cancellationToken = default)
    {
        if (Transport(transport) == FileTransport.Sftp)
        {
            await _sftp.UploadAsync(new MemoryStream(file.ToArray(), writable: false), path, null, cancellationToken)
                .ConfigureAwait(false);
            return;
        }

        using var form = new MultipartFormDataContent();
        form.Add(FilePart(new ReadOnlyMemoryContent(file), "file", Basename(path)));
        AssertUploaded(await _api.PostContentAsync<List<UploadResult>>(
            await RouteAsync("/upload", cancellationToken).ConfigureAwait(false), form,
            new Query { ["path"] = path }, cancellationToken).ConfigureAwait(false));
    }

    /// <summary>Writes <paramref name="content"/> as UTF-8 to <paramref name="path"/>, creating parent folders.</summary>
    public Task UploadFileAsync(string content, string path, FileTransport? transport = null,
        CancellationToken cancellationToken = default) =>
        UploadFileAsync(Encoding.UTF8.GetBytes(content), path, transport, cancellationToken);

    /// <summary>
    /// mogenius only: writes what <paramref name="source"/> yields to <paramref name="path"/> without holding it in
    /// memory — for files too big for a byte array, e.g. a <see cref="FileStream"/> or a response body. The upload
    /// is sent chunked, creating parent folders.
    /// </summary>
    /// <param name="source">read to its end; not disposed</param>
    /// <param name="path">destination inside the container</param>
    /// <param name="progress">hears how many bytes were read from the source so far</param>
    /// <param name="transport">as for <see cref="UploadFileAsync(ReadOnlyMemory{byte},string,FileTransport?,CancellationToken)"/></param>
    /// <param name="cancellationToken">cancels the upload</param>
    public async Task UploadFileStreamAsync(Stream source, string path, IProgress<UploadProgress>? progress = null,
        FileTransport? transport = null, CancellationToken cancellationToken = default)
    {
        if (Transport(transport) == FileTransport.Sftp)
        {
            await _sftp.UploadAsync(source, path, progress, cancellationToken).ConfigureAwait(false);
            return;
        }

        using var form = new MultipartFormDataContent($"----mogenius-{Guid.NewGuid()}");
        // a non-seekable stream of unknown length goes out chunked
        form.Add(FilePart(new StreamContent(new ProgressStream(source, progress)), "file", Basename(path)));
        AssertUploaded(await _api.PostContentAsync<List<UploadResult>>(
            await RouteAsync("/upload", cancellationToken).ConfigureAwait(false), form,
            new Query { ["path"] = path }, cancellationToken).ConfigureAwait(false));
    }

    /// <summary>
    /// Several files in one request; each failure is reported, the others still land. Over SFTP (mogenius:
    /// <paramref name="transport"/>) the files go one after the other through the sandbox's SFTP session.
    /// </summary>
    public async Task<IReadOnlyList<UploadResult>> UploadFilesAsync(IReadOnlyList<FileUpload> files,
        FileTransport? transport = null, CancellationToken cancellationToken = default)
    {
        if (files.Count == 0)
        {
            return [];
        }

        if (Transport(transport) == FileTransport.Sftp)
        {
            return await UploadFilesOverSftpAsync(files, cancellationToken).ConfigureAwait(false);
        }

        using var form = new MultipartFormDataContent();
        foreach (var upload in files)
        {
            // the part's field name is the destination: multipart parsers keep field names verbatim but reduce
            // file names to their base name
            form.Add(FilePart(new ReadOnlyMemoryContent(upload.Source), upload.Destination,
                Basename(upload.Destination)));
        }

        return await _api.PostContentAsync<List<UploadResult>>(
            await RouteAsync("/upload", cancellationToken).ConfigureAwait(false), form,
            ct: cancellationToken).ConfigureAwait(false) ?? [];
    }

    /// <summary><see cref="UploadFilesAsync"/> over SFTP: one after the other, one result each.</summary>
    private async Task<IReadOnlyList<UploadResult>> UploadFilesOverSftpAsync(IReadOnlyList<FileUpload> files,
        CancellationToken ct)
    {
        var results = new List<UploadResult>();
        for (var index = 0; index < files.Count; index++)
        {
            var upload = files[index];
            try
            {
                var path = await _sftp.UploadAsync(new MemoryStream(upload.Source.ToArray(), writable: false),
                    upload.Destination, null, ct).ConfigureAwait(false);
                results.Add(new UploadResult(path, true, null));
            }
            catch (MogeniusException err) when (index == 0 &&
                                                err.ErrorCode == MogeniusErrorCodes.SftpUnavailable)
            {
                // nothing can land: the call fails as the HTTP upload's request would
                throw;
            }
            catch (Exception err) when (err is not OperationCanceledException)
            {
                results.Add(new UploadResult(upload.Destination, false, err.Message));
            }
        }

        return results;
    }

    /// <summary>Creates the folder and its parents. <paramref name="mode"/> is octal (<c>755</c>) or symbolic (<c>u+x</c>).</summary>
    public async Task CreateFolderAsync(string path, string? mode = null, CancellationToken cancellationToken = default) =>
        await _api.PostAsync<JsonElement?>(await RouteAsync("/folder", cancellationToken).ConfigureAwait(false), null,
            new Query { ["path"] = path, ["mode"] = mode }, cancellationToken).ConfigureAwait(false);

    /// <summary>Moves or renames; the destination's folder must exist.</summary>
    public async Task MoveFilesAsync(string source, string destination, CancellationToken cancellationToken = default) =>
        await _api.PostAsync<JsonElement?>(await RouteAsync("/move", cancellationToken).ConfigureAwait(false), null,
            new Query { ["source"] = source, ["destination"] = destination }, cancellationToken).ConfigureAwait(false);

    /// <summary>Removes a file, or a folder — only when empty unless <paramref name="recursive"/>.</summary>
    public async Task DeleteFileAsync(string path, bool recursive = false,
        CancellationToken cancellationToken = default) =>
        await _api.DeleteAsync<JsonElement?>(await RouteAsync("", cancellationToken).ConfigureAwait(false),
            new Query { ["path"] = path, ["recursive"] = recursive ? "true" : "false" }, cancellationToken)
            .ConfigureAwait(false);

    /// <summary>Mode, owner and group are independent; what is not given stays.</summary>
    public async Task<SandboxFileInfo> SetFilePermissionsAsync(string path, SetFilePermissionsParams parameters,
        CancellationToken cancellationToken = default) =>
        await _api.PostAsync<SandboxFileInfo>(await RouteAsync("/permissions", cancellationToken).ConfigureAwait(false),
            null, new Query
            {
                ["path"] = path,
                ["mode"] = parameters.Mode,
                ["owner"] = parameters.Owner,
                ["group"] = parameters.Group,
            }, cancellationToken).ConfigureAwait(false);

    /// <summary>Names below <paramref name="path"/> matching a shell glob, e.g. <c>*.py</c>.</summary>
    public async Task<SearchFilesResponse> SearchFilesAsync(string path, string pattern,
        CancellationToken cancellationToken = default) =>
        await _api.GetAsync<SearchFilesResponse>(await RouteAsync("/search", cancellationToken).ConfigureAwait(false),
            new Query { ["path"] = path, ["pattern"] = pattern }, cancellationToken).ConfigureAwait(false);

    /// <summary>Lines below <paramref name="path"/> matching a grep pattern.</summary>
    public async Task<IReadOnlyList<FileMatch>> FindFilesAsync(string path, string pattern,
        CancellationToken cancellationToken = default)
    {
        var result = await _api.GetAsync<FindBody>(await RouteAsync("/find", cancellationToken).ConfigureAwait(false),
            new Query { ["path"] = path, ["pattern"] = pattern }, cancellationToken).ConfigureAwait(false);
        return result?.Matches ?? [];
    }

    /// <summary>Replaces every literal occurrence of <paramref name="pattern"/> in each file; one result per file.</summary>
    public async Task<IReadOnlyList<ReplaceResult>> ReplaceInFilesAsync(IReadOnlyList<string> files, string pattern,
        string newValue, CancellationToken cancellationToken = default) =>
        await _api.PostAsync<List<ReplaceResult>>(await RouteAsync("/replace", cancellationToken).ConfigureAwait(false),
            new { files, pattern, newValue }, ct: cancellationToken).ConfigureAwait(false) ?? [];

    /// <summary>The call's transport, else the client's.</summary>
    private FileTransport Transport(FileTransport? transport)
    {
        var chosen = transport ?? _api.FileTransport;
        if (!Enum.IsDefined(chosen))
        {
            throw new MogeniusValidationException($"The upload transport is \"{chosen}\"; it is http or sftp.");
        }

        return chosen;
    }

    /// <summary>The pod route of <paramref name="suffix"/>, in the sandbox's own container (the pod route would take the first).</summary>
    private async Task<string> RouteAsync(string suffix, CancellationToken ct)
    {
        var pod = await _pod(ct).ConfigureAwait(false);
        var basePath =
            $"/resource/files/{Uri.EscapeDataString(_namespace)}/{Uri.EscapeDataString(pod.PodName)}{suffix}";
        return string.IsNullOrEmpty(pod.ContainerName)
            ? basePath
            : $"{basePath}?container={Uri.EscapeDataString(pod.ContainerName)}";
    }

    /// <summary>A form part with quoted field and file names, quotes and line breaks percent-encoded.</summary>
    private static HttpContent FilePart(HttpContent content, string name, string fileName)
    {
        static string Quote(string value) =>
            $"\"{value.Replace("\"", "%22", StringComparison.Ordinal).Replace("\r", "%0D", StringComparison.Ordinal).Replace("\n", "%0A", StringComparison.Ordinal)}\"";

        content.Headers.ContentType = new MediaTypeHeaderValue("application/octet-stream");
        content.Headers.ContentDisposition = new ContentDispositionHeaderValue("form-data")
        {
            Name = Quote(name),
            FileName = Quote(fileName),
        };
        return content;
    }

    internal static string Basename(string path)
    {
        var trimmed = path.TrimEnd('/');
        var index = trimmed.LastIndexOf('/');
        return index >= 0 ? trimmed[(index + 1)..] : trimmed;
    }

    private static void AssertUploaded(IReadOnlyList<UploadResult>? results)
    {
        var failed = results?.FirstOrDefault(result => !result.Success);
        if (failed is not null)
        {
            throw new MogeniusException($"Upload to {failed.Path} failed: {failed.Error ?? "unknown error"}",
                source: ErrorSource.Operator);
        }
    }

    private sealed record DownloadLinkBody(string Url, int ExpiresInSeconds);

    private sealed record FindBody(List<FileMatch>? Matches, bool Truncated);
}

/// <summary>Reads a source and reports how many bytes went through.</summary>
internal sealed class ProgressStream(Stream inner, IProgress<UploadProgress>? progress) : Stream
{
    private long _sent;

    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => throw new NotSupportedException();

    public override long Position
    {
        get => throw new NotSupportedException();
        set => throw new NotSupportedException();
    }

    public override int Read(byte[] buffer, int offset, int count) => Count(inner.Read(buffer, offset, count));

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default) =>
        Count(await inner.ReadAsync(buffer, cancellationToken).ConfigureAwait(false));

    public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
        ReadAsync(buffer.AsMemory(offset, count), cancellationToken).AsTask();

    private int Count(int read)
    {
        if (read > 0)
        {
            _sent += read;
            progress?.Report(new UploadProgress(_sent));
        }

        return read;
    }

    public override void Flush()
    {
    }

    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();
}

/// <summary>
/// A download that resumes on its own. It counts the bytes it handed out; when the body fails or ends before the
/// announced length, it asks the link again for the rest with <c>Range</c> and the file's ETag in <c>If-Range</c>.
/// Only a 206 that continues exactly at that byte is spliced in: a 200 means the file changed (or the platform cannot
/// resume), and since bytes already went out the stream fails instead of repeating them.
/// </summary>
internal sealed class ResumableDownloadStream : Stream
{
    private readonly ApiClient _api;
    private readonly string _url;
    private readonly TimeSpan[] _delays;
    private readonly IProgress<DownloadProgress>? _progress;
    private readonly long? _total;
    private readonly string? _etag;
    private readonly bool _resumable;
    private HttpResponseMessage _response;
    private Stream _body;
    private long _received;
    private int _attempts;

    private ResumableDownloadStream(ApiClient api, string url, HttpResponseMessage response, Stream body,
        TimeSpan[] delays, IProgress<DownloadProgress>? progress)
    {
        _api = api;
        _url = url;
        _response = response;
        _body = body;
        _delays = delays;
        _progress = progress;
        _total = response.Content.Headers.ContentLength;
        _etag = response.Headers.ETag?.ToString();
        _resumable = response.Headers.AcceptRanges.Contains("bytes") && _etag is not null && _total is not null;
    }

    public static async Task<ResumableDownloadStream> CreateAsync(ApiClient api, string url,
        HttpResponseMessage first, TimeSpan[] delays, IProgress<DownloadProgress>? progress, CancellationToken ct)
    {
        var body = await first.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
        return new ResumableDownloadStream(api, url, first, body, delays, progress);
    }

    public override bool CanRead => true;
    public override bool CanSeek => false;
    public override bool CanWrite => false;
    public override long Length => _total ?? throw new NotSupportedException();

    public override long Position
    {
        get => _received;
        set => throw new NotSupportedException();
    }

    public override int Read(byte[] buffer, int offset, int count) =>
        ReadAsync(buffer.AsMemory(offset, count)).AsTask().GetAwaiter().GetResult();

    public override Task<int> ReadAsync(byte[] buffer, int offset, int count, CancellationToken cancellationToken) =>
        ReadAsync(buffer.AsMemory(offset, count), cancellationToken).AsTask();

    public override async ValueTask<int> ReadAsync(Memory<byte> buffer, CancellationToken cancellationToken = default)
    {
        if (buffer.Length == 0)
        {
            return 0;
        }

        while (true)
        {
            int read;
            try
            {
                read = await _body.ReadAsync(buffer, cancellationToken).ConfigureAwait(false);
            }
            catch (Exception err) when (err is IOException or HttpRequestException &&
                                        !cancellationToken.IsCancellationRequested)
            {
                await ResumeAsync(err.Message, cancellationToken).ConfigureAwait(false);
                continue;
            }

            if (read == 0)
            {
                if (_resumable && _received < _total)
                {
                    await ResumeAsync("the connection ended early", cancellationToken).ConfigureAwait(false);
                    continue;
                }

                return 0;
            }

            _received += read;
            _progress?.Report(new DownloadProgress(_received, _total ?? 0));
            return read;
        }
    }

    private async Task ResumeAsync(string cause, CancellationToken ct)
    {
        if (!_resumable || _attempts >= _delays.Length)
        {
            throw new MogeniusException(_resumable
                ? $"Download stopped at byte {_received} of {_total} and could not be resumed: {cause}"
                : $"Download stopped at byte {_received}: {cause}", source: ErrorSource.Sdk);
        }

        await Task.Delay(_delays[_attempts], ct).ConfigureAwait(false);
        _attempts++;
        var response = await _api.FetchDownloadAsync(_url, _received, _etag, ct).ConfigureAwait(false);
        var range = response.Content.Headers.ContentRange;
        if (response.StatusCode != HttpStatusCode.PartialContent || range?.From != _received)
        {
            var status = (int)response.StatusCode;
            response.Dispose();
            throw new MogeniusException(
                $"Download could not continue at byte {_received}: the file changed or the platform cannot resume (HTTP {status}).",
                source: ErrorSource.Sdk);
        }

        await _body.DisposeAsync().ConfigureAwait(false);
        _response.Dispose();
        _response = response;
        _body = await response.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
    }

    public override void Flush()
    {
    }

    public override long Seek(long offset, SeekOrigin origin) => throw new NotSupportedException();
    public override void SetLength(long value) => throw new NotSupportedException();
    public override void Write(byte[] buffer, int offset, int count) => throw new NotSupportedException();

    protected override void Dispose(bool disposing)
    {
        if (disposing)
        {
            _body.Dispose();
            _response.Dispose();
        }

        base.Dispose(disposing);
    }
}
