import type { ApiClient } from './api-client.js';
import {
  MogeniusConflictError,
  MogeniusTimeoutError,
  MogeniusUnsupportedError,
  MogeniusValidationError,
} from './errors.js';
import { FileSystem } from './file-system.js';
import { Process } from './process.js';
import { openTunnel } from './tunnel.js';
import type { CodeLanguage, ExecuteResponse, SandboxInfo, SandboxState, Tunnel, TunnelOptions } from './types.js';

/** How often `waitUntil…` re-reads the sandbox. */
const POLL_INTERVAL_MS = 1000;
/** Default for `waitUntilStarted`. */
const DEFAULT_WAIT_SECONDS = 60;

/**
 * A handle to one sandbox: its current data plus the actions on it. The
 * fields are plain getters; `refreshData()` reloads them from the
 * platform. `process` runs commands and code inside, `fs` reads and writes
 * its files.
 */
export class Sandbox {
  readonly process: Process;
  /** Files inside the sandbox. */
  readonly fs: FileSystem;
  private info: SandboxInfo;

  constructor(
    private readonly api: ApiClient,
    info: SandboxInfo,
    private readonly language: CodeLanguage = 'python',
  ) {
    this.info = info;
    this.process = new Process(api, info.namespace, language, () => this.pod());
    this.fs = new FileSystem(api, info.namespace, () => this.pod());
  }

  /*******************************************************************************************************************
   * fields
   ******************************************************************************************************************/

  /** Name of the claim or sandbox object; what every route takes. */
  get id(): string {
    return this.info.id;
  }
  get name(): string {
    return this.info.name;
  }
  get state(): SandboxState {
    return this.info.state;
  }
  /** Why the state is `error` or still `starting`. */
  get errorReason(): string | null {
    return this.info.stateReason;
  }
  get labels(): Record<string, string> {
    return this.info.labels;
  }
  /** Profile the sandbox was made from (`snapshot`). */
  get snapshot(): string | null {
    return this.info.profile;
  }
  get image(): string | null {
    return this.info.image;
  }
  /** Kubernetes namespace (`target`). */
  get target(): string {
    return this.info.namespace;
  }
  get namespace(): string {
    return this.info.namespace;
  }
  get createdAt(): string | null {
    return this.info.createdAt;
  }
  /** mogenius: when the sandbox shuts down; null without a deadline. */
  get expiresAt(): string | null {
    return this.info.expiresAt;
  }
  /** mogenius: pod and container that commands, files and sessions address. */
  get podName(): string | null {
    return this.info.podName;
  }
  get containerName(): string {
    return this.info.containerName;
  }
  /** mogenius: the sandbox's address inside the cluster; null while there is none. */
  get serviceFQDN(): string | null {
    return this.info.serviceFQDN;
  }
  /** mogenius: the Sandbox object that runs the pod; null while a claim is unbound. */
  get sandboxName(): string | null {
    return this.info.sandboxName;
  }
  /** mogenius: `Running` or `Suspended`. */
  get operatingMode(): string | null {
    return this.info.operatingMode;
  }
  /** mogenius: who created the sandbox. */
  get createdBy(): string | null {
    return this.info.createdBy;
  }
  /** Everything the platform knows, as returned. */
  get data(): SandboxInfo {
    return this.info;
  }

  /*******************************************************************************************************************
   * lifecycle
   ******************************************************************************************************************/

  /**
   * Pod and container the pod routes address. A sandbox created without
   * waiting has no pod at first, so one without is read again before giving up.
   */
  private async pod(): Promise<{ podName: string; containerName: string }> {
    if (!this.info.podName) {
      await this.refreshData();
    }
    const podName = this.info.podName;
    if (!podName) {
      throw new MogeniusConflictError(`Sandbox ${this.id} has no pod yet: wait until it is started.`);
    }
    return { podName, containerName: this.info.containerName };
  }

  /** Reloads the sandbox from the platform. */
  async refreshData(): Promise<void> {
    this.info = await this.api.get<SandboxInfo>(this.path());
  }

  /** Resumes a stopped sandbox (`operatingMode: Running`) and waits for it. */
  async start(timeout: number = DEFAULT_WAIT_SECONDS): Promise<void> {
    this.info = await this.api.post<SandboxInfo>(`${this.path()}/start`);
    await this.waitUntilStarted(timeout);
  }

  /** Suspends the sandbox: the pod goes, the volume stays. */
  async stop(timeout: number = DEFAULT_WAIT_SECONDS): Promise<void> {
    this.info = await this.api.post<SandboxInfo>(`${this.path()}/stop`);
    await this.waitUntilStopped(timeout);
  }

  /** Deletes the sandbox. With `ephemeral` (default) pod and storage go with it. */
  async delete(): Promise<void> {
    this.info = await this.api.delete<SandboxInfo>(this.path());
  }

  /** Polls until the sandbox is `started`; throws on `error` or when the time runs out. */
  async waitUntilStarted(timeout: number = DEFAULT_WAIT_SECONDS): Promise<void> {
    await this.waitFor('started', timeout);
  }

  /** Polls until the sandbox is `stopped`. */
  async waitUntilStopped(timeout: number = DEFAULT_WAIT_SECONDS): Promise<void> {
    await this.waitFor('stopped', timeout);
  }

  /** Replaces the sandbox's labels (platform labels stay). */
  async setLabels(labels: Record<string, string>): Promise<Record<string, string>> {
    this.info = await this.api.patch<SandboxInfo>(this.path(), { labels });
    return this.info.labels;
  }

  /** Lifetime in minutes from now; 0 removes the deadline. */
  async setAutoDeleteInterval(minutes: number): Promise<void> {
    this.info = await this.api.patch<SandboxInfo>(this.path(), { ttlMinutes: Math.max(0, Math.floor(minutes)) });
  }

  /** Alias of `setAutoDeleteInterval`. */
  async setTtl(minutes: number): Promise<void> {
    await this.setAutoDeleteInterval(minutes);
  }

  /** Home directory of the container user, asked from the running sandbox. */
  async getUserRootDir(): Promise<string> {
    return this.oneLine('printf %s "$HOME"');
  }

  /** The container's working directory. */
  async getWorkDir(): Promise<string> {
    return this.oneLine('pwd');
  }

  /**
   * mogenius: forwards a local port to `port` inside the sandbox through the
   * platform's stream gateway, the way a port-forward does — for an HTTP
   * server, a WebSocket or a database in the sandbox. HTTP, WebSockets, gRPC
   * and plain TCP all pass; only bytes travel, the sandbox sees no
   * credentials. Node only. Needs `organizationId`, `clusterId` and the
   * stream gateway; close the tunnel when done.
   */
  async tunnel(port: number, options: TunnelOptions = {}): Promise<Tunnel> {
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      throw new MogeniusValidationError(`Port ${port} is not a TCP port.`, { source: 'sdk' });
    }
    const { podName } = await this.pod();
    return openTunnel(this.api, { namespace: this.info.namespace, podName, port }, options);
  }

  /*******************************************************************************************************************
   * not available on Kubernetes sandboxes
   ******************************************************************************************************************/

  /** Idle shutdown is not implemented yet (MOG-4697); the lifetime deadline is `setAutoDeleteInterval`. */
  setAutostopInterval(_minutes: number): Promise<void> {
    return Promise.reject(
      new MogeniusUnsupportedError('Auto-stop on idle', 'Use setAutoDeleteInterval() for a fixed lifetime.'),
    );
  }

  setAutoArchiveInterval(_minutes: number): Promise<void> {
    return Promise.reject(
      new MogeniusUnsupportedError('Archiving', 'Delete the sandbox, or keep it stopped: the volume stays.'),
    );
  }

  archive(): Promise<void> {
    return Promise.reject(new MogeniusUnsupportedError('Archiving', 'Stop the sandbox instead; its volume stays.'));
  }

  fork(): Promise<Sandbox> {
    return Promise.reject(
      new MogeniusUnsupportedError('Forking a running sandbox', 'Create a new sandbox from the same profile.'),
    );
  }

  /*******************************************************************************************************************
   * helpers
   ******************************************************************************************************************/

  private path(): string {
    return `/sandbox/${encodeURIComponent(this.info.namespace)}/${encodeURIComponent(this.info.id)}`;
  }

  private async oneLine(command: string): Promise<string> {
    const response: ExecuteResponse = await this.process.executeCommand(command);
    if (response.exitCode !== 0) {
      throw new MogeniusConflictError(
        `"${command}" failed in sandbox ${this.id} with exit code ${response.exitCode}: ${response.result.trim()}`,
        {
          source: 'sdk',
        },
      );
    }
    return (response.artifacts?.stdout ?? response.result).trim();
  }

  private async waitFor(target: SandboxState, timeoutSeconds: number): Promise<void> {
    const deadline = Date.now() + Math.max(0, timeoutSeconds) * 1000;
    for (;;) {
      if (this.info.state === target) {
        return;
      }
      if (this.info.state === 'error') {
        throw new MogeniusConflictError(
          `Sandbox ${this.id} is in error state: ${this.info.stateReason ?? 'no reason reported'}`,
          {
            errorCode: 'SANDBOX_ERROR',
            source: 'sdk',
          },
        );
      }
      if (this.info.state === 'destroyed' || this.info.state === 'destroying') {
        throw new MogeniusConflictError(`Sandbox ${this.id} is being destroyed`, { source: 'sdk' });
      }
      if (Date.now() >= deadline) {
        throw new MogeniusTimeoutError(
          `Sandbox ${this.id} did not reach state "${target}" within ${timeoutSeconds} s (still "${this.info.state}")`,
          {
            source: 'sdk',
          },
        );
      }
      await sleep(Math.min(POLL_INTERVAL_MS, Math.max(0, deadline - Date.now())));
      await this.refreshData();
    }
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
