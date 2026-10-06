/**
 * Public types of `@mogenius/sandbox`. Names and shapes follow what agent frameworks expect
 * wherever the behaviour is the same, so a ported program keeps compiling
 * after the import swap; mogenius-only fields are marked as such.
 */

/** How the SDK reaches the platform. Every field falls back to an environment variable. */
export interface MogeniusConfig {
  /** Personal access token or API key (`mo_pat:…`). Env: `MOGENIUS_API_KEY`. */
  apiKey?: string;
  /** Platform API base URL. Env: `MOGENIUS_API_URL`. Default: https://platform-api.mogenius.com */
  apiUrl?: string;
  /** Organization the key acts in. Env: `MOGENIUS_ORGANIZATION_ID`. Optional for keys with one organization scope. */
  organizationId?: string;
  /** Cluster the sandboxes run on. Env: `MOGENIUS_CLUSTER_ID`. Optional for keys with one cluster scope. */
  clusterId?: string;
  /**
   * Kubernetes namespace the sandbox chart puts sandboxes in
   * (`sandboxes.namespace.name`). Env: `MOGENIUS_SANDBOX_NAMESPACE`. Default `agent-sandbox`.
   * `target` maps here.
   */
  namespace?: string;
  /** Alias of `namespace`, for programs that pass `target`. */
  target?: string;
  /** Workspace to act through when the key has no cluster role. Header `workspace-name`. */
  workspaceName?: string;
  /** Replaces the global `fetch`; for tests and custom agents. */
  fetch?: typeof fetch;
}

/** Everything a Sandbox needs from the config, resolved. */
export interface ResolvedConfig {
  apiKey: string;
  apiUrl: string;
  organizationId: string | undefined;
  clusterId: string | undefined;
  namespace: string;
  workspaceName: string | undefined;
  fetch: typeof fetch;
}

/** Languages `codeRun` knows how to launch. */
export type CodeLanguage = 'python' | 'typescript' | 'javascript';

/** Shared `create()` parameters. */
export interface CreateSandboxBaseParams {
  /** Name of the sandbox; generated from the profile when absent. */
  name?: string;
  /** Default language for `codeRun`. Default `python`. */
  language?: CodeLanguage;
  /** Kubernetes labels; `list()` and `findOne()` filter by them. */
  labels?: Record<string, string>;
  /** Environment for the sandbox container. On a warm-pool sandbox the profile must allow env injection. */
  envVars?: Record<string, string>;
  /** Minutes until the sandbox is shut down; absent means no deadline. */
  autoDeleteInterval?: number;
  /** Delete pod and storage when the lifetime ends or the sandbox is deleted (default true). */
  ephemeral?: boolean;
  /** Accepted for compatibility; mogenius has no idle timer yet (MOG-4697) and takes resources from the profile. */
  autoStopInterval?: number;
  autoArchiveInterval?: number;
}

/** Create from a profile (`snapshot`). The profile's warm pool answers in seconds. */
export interface CreateSandboxFromSnapshotParams extends CreateSandboxBaseParams {
  /** Profile = SandboxTemplate and SandboxWarmPool of that name. Default `default`. */
  snapshot?: string;
}

/** Create from an image: a Sandbox stamped from the profile's template with this image. A pod start. */
export interface CreateSandboxFromImageParams extends CreateSandboxBaseParams {
  image: string;
  /** Profile whose pod template is used around the image. Default `default`. */
  snapshot?: string;
}

export type CreateSandboxParams = CreateSandboxFromSnapshotParams | CreateSandboxFromImageParams;

/** Options of `create()`. */
export interface CreateSandboxOptions {
  /** Seconds to wait for the sandbox to start. Default 60. 0 returns right after creation. */
  timeout?: number;
}

/** Sandbox states as the platform reports them. */
export type SandboxState =
  'creating' | 'starting' | 'started' | 'stopping' | 'stopped' | 'error' | 'destroying' | 'destroyed';

/** Filter of `findOne()` and `list()`. */
export interface SandboxFilter {
  id?: string;
  labels?: Record<string, string>;
  state?: SandboxState;
}

/** One page of `list()`. */
export interface PaginatedSandboxes<T> {
  items: T[];
  /** Hand to the next `list()` call as `cursor`. Null on the last page. */
  nextCursor: string | null;
  total: number;
}

/** Options of `list()`. */
export interface ListSandboxesOptions {
  limit?: number;
  cursor?: string;
}

/** Outcome of `executeCommand()` and `codeRun()`. */
export interface ExecuteResponse {
  /** Exit code of the command; 0 means success. */
  exitCode: number;
  /** Combined output: stdout followed by stderr. */
  result: string;
  artifacts?: ExecutionArtifacts;
}

export interface ExecutionArtifacts {
  stdout: string;
  /** stderr kept apart from `stdout`. */
  stderr: string;
  /** Charts matplotlib printed as artifacts; filled by `codeRun()` for Python. */
  charts?: Chart[];
}

/** A chart `codeRun()` extracted from the output. */
export interface Chart {
  type: string;
  title?: string;
  /** PNG, base64-encoded. */
  png?: string;
  elements?: unknown[];
}

/** Options of `codeRun()`. */
export interface CodeRunParams {
  /** Arguments passed to the script (`sys.argv[1..]`, `process.argv[2..]`). */
  argv?: string[];
  env?: Record<string, string>;
  /** Overrides the sandbox's default language for this run. */
  language?: CodeLanguage;
}

/** The sandbox as the platform returns it. */
export interface SandboxInfo {
  id: string;
  name: string;
  namespace: string;
  /** Whether `id` names a SandboxClaim (warm pool) or a Sandbox (own image). */
  kind: 'SandboxClaim' | 'Sandbox';
  state: SandboxState;
  stateReason: string | null;
  labels: Record<string, string>;
  /** Profile the sandbox was made from (`snapshot`). */
  profile: string | null;
  image: string | null;
  /** The pod's name; null while creating. */
  podName: string | null;
  sandboxName: string | null;
  containerName: string;
  serviceFQDN: string | null;
  operatingMode: string | null;
  ephemeral: boolean;
  /** ISO timestamp the sandbox is shut down at; null without a deadline. */
  expiresAt: string | null;
  createdAt: string | null;
  createdBy: string | null;
}

/** Error body every sandbox route answers with. */
export interface ErrorBody {
  statusCode?: number;
  errorCode?: string;
  source?: 'api' | 'operator';
  message?: string | string[];
  error?: string;
}

/*********************************************************************************************************************
 * files
 ********************************************************************************************************************/

/** One file or folder. */
export interface FileInfo {
  name: string;
  /** mogenius only: absolute path inside the container. */
  path: string;
  isDir: boolean;
  size: number;
  /** ISO timestamp. */
  modTime: string;
  /** Octal, e.g. `0644`. */
  mode: string;
  /** `ls -l` style, e.g. `-rw-r--r--`. */
  permissions: string;
  owner: string;
  group: string;
  /** mogenius only: sniffed media type of a regular file. */
  mimeType?: string;
}

/** One entry of `uploadFiles()`; the source is bytes or text, not a local file path. */
export interface FileUpload {
  source: Buffer | Uint8Array | string;
  /** Destination path inside the container. */
  destination: string;
}

export interface UploadResult {
  path: string;
  success: boolean;
  error: string | null;
}

/** Result of `searchFiles()`. */
export interface SearchFilesResponse {
  files: string[];
  /** mogenius only: more matched than were returned. */
  truncated: boolean;
}

/** One matching line of `findFiles()`. */
export interface Match {
  file: string;
  line: number;
  content: string;
}

/** One file's outcome of `replaceInFiles()`. */
export interface ReplaceResult {
  file: string;
  success: boolean;
  error: string | null;
}

/** mogenius only: the one-time URL behind a streamed download. */
export interface DownloadLink {
  url: string;
  /** Seconds the link stays valid; it is spent on first use either way. */
  expiresInSeconds: number;
}

/** Parameters of `setFilePermissions()`. */
export interface SetFilePermissionsParams {
  /** Octal (`755`) or symbolic (`u+x`). */
  mode?: string;
  owner?: string;
  group?: string;
}
