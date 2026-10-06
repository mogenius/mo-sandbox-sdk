import type { ApiClient } from './api-client.js';
import { MogeniusError, MogeniusNotFoundError } from './errors.js';
import type {
  DownloadLink,
  FileInfo,
  FileUpload,
  Match,
  ReplaceResult,
  SearchFilesResponse,
  SetFilePermissionsParams,
  UploadResult,
} from './types.js';

/** What the platform answers for one file entry. */
interface FileInfoBody {
  name: string;
  path: string;
  isDir: boolean;
  size: number;
  modTime: string;
  mode: string;
  permissions: string;
  owner: string;
  group: string;
  mimeType: string | null;
}

/**
 * The file system of a sandbox. Paths are absolute inside the container
 * or relative to its working directory. Every call goes through the platform
 * API to the operator, which runs the file operation inside the pod; nothing
 * is installed in the image for it.
 */
export class FileSystem {
  constructor(
    private readonly api: ApiClient,
    private readonly namespace: string,
    private readonly sandboxId: string,
  ) {}

  /** Entries directly below `path` (default: the working directory). */
  async listFiles(path?: string): Promise<FileInfo[]> {
    const items = await this.api.get<FileInfoBody[]>(this.route(''), { path });
    return items.map(toFileInfo);
  }

  async getFileDetails(path: string): Promise<FileInfo> {
    return toFileInfo(await this.api.get<FileInfoBody>(this.route('/info'), { path }));
  }

  /** The file's bytes. A folder comes back as a gzipped tar. */
  async downloadFile(path: string): Promise<Buffer> {
    const stream = await this.downloadFileStream(path);
    return Buffer.from(await new Response(stream).arrayBuffer());
  }

  /**
   * mogenius only: the file as a stream, chunked from the container to the
   * caller without any station holding it whole — for files too big for a
   * Buffer. Pipe it to disk with `Readable.fromWeb(stream).pipe(createWriteStream(…))`.
   * The platform hands out a short-lived link that is fetched without auth
   * headers; a platform without streamed downloads answers the buffered way.
   *
   * A file (not a folder) resumes on its own: when the connection drops or
   * ends early, the stream asks the same link for the rest with a Range
   * request and carries on, up to five times with growing pauses. The bytes
   * the caller reads stay one uninterrupted, complete file.
   */
  async downloadFileStream(path: string): Promise<ReadableStream<Uint8Array>> {
    let link: DownloadLink;
    try {
      link = await this.api.post<DownloadLink>(this.route('/download-link'), undefined, { path });
    } catch (err) {
      if (isRouteMissing(err)) {
        const data = await this.api.getBinary(this.route('/download'), { path });
        return new Blob([data as Uint8Array<ArrayBuffer>]).stream();
      }
      throw err;
    }
    const first = await this.api.fetchDownload(link.url);
    return resumableStream(this.api, link.url, first, this.resumeDelaysMs);
  }

  /** Pauses before each resume attempt; tests shorten them. */
  private readonly resumeDelaysMs: readonly number[] = [1000, 2000, 4000, 8000, 16000];

  /**
   * Writes `file` to `path`, creating parent folders. A string is written as
   * UTF-8; a local file path is not accepted here, which the browser-safe
   * SDK does not — read the file yourself and pass the Buffer.
   */
  async uploadFile(file: Buffer | Uint8Array | string, path: string): Promise<void> {
    const form = new FormData();
    form.append('file', toBlob(file), basename(path));
    const results = await this.api.postForm<UploadResult[]>(this.route('/upload'), form, { path });
    assertUploaded(results);
  }

  /** Several files in one request; each failure is reported, the others still land. */
  async uploadFiles(files: FileUpload[]): Promise<UploadResult[]> {
    if (files.length === 0) {
      return [];
    }
    const form = new FormData();
    for (const upload of files) {
      // the part's field name is the destination: multipart parsers keep field names
      // verbatim but reduce file names to their base name
      form.append(upload.destination, toBlob(upload.source), basename(upload.destination));
    }
    return this.api.postForm<UploadResult[]>(this.route('/upload'), form);
  }

  /** Downloads several files; a failure for one does not stop the others. */
  async downloadFiles(paths: string[]): Promise<{ path: string; data?: Buffer; error?: string }[]> {
    return Promise.all(
      paths.map(async (path) => {
        try {
          return { path, data: await this.downloadFile(path) };
        } catch (err) {
          return { path, error: (err as Error).message };
        }
      }),
    );
  }

  /** Creates the folder and its parents. `mode` is octal (`755`) or symbolic (`u+x`). */
  async createFolder(path: string, mode?: string): Promise<void> {
    await this.api.post<void>(this.route('/folder'), undefined, { path, mode });
  }

  /** Moves or renames; the destination's folder must exist. */
  async moveFiles(source: string, destination: string): Promise<void> {
    await this.api.post<void>(this.route('/move'), undefined, { source, destination });
  }

  /** Removes a file, or a folder — only when empty unless `recursive`. */
  async deleteFile(path: string, recursive = false): Promise<void> {
    await this.api.delete<void>(this.route(''), { path, recursive: recursive ? 'true' : 'false' });
  }

  /** Mode, owner and group are independent; what is not given stays. */
  async setFilePermissions(path: string, params: SetFilePermissionsParams): Promise<FileInfo> {
    return toFileInfo(
      await this.api.post<FileInfoBody>(this.route('/permissions'), undefined, {
        path,
        mode: params.mode,
        owner: params.owner,
        group: params.group,
      }),
    );
  }

  /** Names below `path` matching a shell glob, e.g. `*.py`. */
  async searchFiles(path: string, pattern: string): Promise<SearchFilesResponse> {
    return this.api.get<SearchFilesResponse>(this.route('/search'), { path, pattern });
  }

  /** Lines below `path` matching a grep pattern. */
  async findFiles(path: string, pattern: string): Promise<Match[]> {
    const result = await this.api.get<{ matches: Match[]; truncated: boolean }>(this.route('/find'), { path, pattern });
    return result.matches;
  }

  /** Replaces every literal occurrence of `pattern` in each file; one result per file. */
  async replaceInFiles(files: string[], pattern: string, newValue: string): Promise<ReplaceResult[]> {
    return this.api.post<ReplaceResult[]>(this.route('/replace'), { files, pattern, newValue });
  }

  private route(suffix: string): string {
    return `/sandbox/${encodeURIComponent(this.namespace)}/${encodeURIComponent(this.sandboxId)}/toolbox/files${suffix}`;
  }
}

function toFileInfo(body: FileInfoBody): FileInfo {
  return {
    name: body.name,
    path: body.path,
    isDir: body.isDir,
    size: body.size,
    modTime: body.modTime,
    mode: body.mode,
    permissions: body.permissions,
    owner: body.owner,
    group: body.group,
    mimeType: body.mimeType ?? undefined,
  };
}

function toBlob(source: Buffer | Uint8Array | string): Blob {
  if (typeof source === 'string') {
    return new Blob([source], { type: 'application/octet-stream' });
  }
  return new Blob([source as Uint8Array<ArrayBuffer>], { type: 'application/octet-stream' });
}

function basename(path: string): string {
  const trimmed = path.replace(/\/+$/, '');
  const index = trimmed.lastIndexOf('/');
  return index >= 0 ? trimmed.slice(index + 1) : trimmed;
}

function assertUploaded(results: UploadResult[]): void {
  const failed = results.find((result) => !result.success);
  if (failed) {
    throw new MogeniusError(`Upload to ${failed.path} failed: ${failed.error ?? 'unknown error'}`, {
      source: 'operator',
    });
  }
}

/** A 404 for the route itself (not for a file): the platform predates streamed downloads. */
function isRouteMissing(err: unknown): boolean {
  return err instanceof MogeniusNotFoundError && err.errorCode === undefined && /^Cannot POST /.test(err.message);
}

/**
 * Wraps a download response in a stream that resumes on its own (MOG-4747).
 * It counts the bytes it handed out; when the body errors or ends before the
 * announced length, it asks `url` again for the rest with `Range` and the
 * file's ETag in `If-Range`. Only a 206 that continues exactly at that byte is
 * spliced in: a 200 means the file changed (or the platform cannot resume),
 * and since bytes already went out the stream fails instead of repeating them.
 */
function resumableStream(
  api: ApiClient,
  url: string,
  first: Response,
  delaysMs: readonly number[],
): ReadableStream<Uint8Array> {
  const total = Number(first.headers.get('content-length') ?? NaN);
  const etag = first.headers.get('etag') ?? undefined;
  const resumable = first.headers.get('accept-ranges') === 'bytes' && etag !== undefined && Number.isFinite(total);
  let reader = first.body!.getReader();
  let received = 0;
  let attempts = 0;

  const resume = async (cause: string): Promise<void> => {
    if (!resumable || attempts >= delaysMs.length) {
      throw new MogeniusError(
        resumable
          ? `Download stopped at byte ${received} of ${total} and could not be resumed: ${cause}`
          : `Download stopped at byte ${received}: ${cause}`,
        { source: 'sdk' },
      );
    }
    await sleep(delaysMs[attempts]!);
    attempts++;
    const response = await api.fetchDownload(url, { offset: received, ifRange: etag });
    const range = /^bytes (\d+)-\d+\/\d+$/.exec(response.headers.get('content-range') ?? '');
    if (response.status !== 206 || !range || Number(range[1]) !== received) {
      await response.body?.cancel().catch(() => undefined);
      throw new MogeniusError(
        `Download could not continue at byte ${received}: the file changed or the platform cannot resume (HTTP ${response.status}).`,
        { source: 'sdk' },
      );
    }
    reader = response.body!.getReader();
  };

  return new ReadableStream<Uint8Array>({
    async pull(controller): Promise<void> {
      for (;;) {
        let chunk: ReadableStreamReadResult<Uint8Array>;
        try {
          chunk = await reader.read();
        } catch (err) {
          await resume((err as Error).message);
          continue;
        }
        if (chunk.done) {
          if (resumable && received < total) {
            await resume('the connection ended early');
            continue;
          }
          controller.close();
          return;
        }
        received += chunk.value.byteLength;
        controller.enqueue(chunk.value);
        return;
      }
    },
    async cancel(reason): Promise<void> {
      await reader.cancel(reason).catch(() => undefined);
    },
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
