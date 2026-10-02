import type { ApiClient } from './api-client.js';
import { MogeniusError } from './errors.js';
import type {
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
 * Daytona's `FileSystem` on a sandbox. Paths are absolute inside the container
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
    return this.api.getBinary(this.route('/download'), { path });
  }

  /**
   * Writes `file` to `path`, creating parent folders. A string is written as
   * UTF-8; Daytona also accepts a local file path here, which the browser-safe
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
