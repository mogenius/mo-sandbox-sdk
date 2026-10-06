import { ApiClient } from './api-client.js';
import { resolveConfig } from './config.js';
import { MogeniusNotFoundError, MogeniusUnsupportedError } from './errors.js';
import { Sandbox } from './sandbox.js';
import type {
  CodeLanguage,
  CreateSandboxOptions,
  CreateSandboxParams,
  ListSandboxesOptions,
  MogeniusConfig,
  PaginatedSandboxes,
  SandboxFilter,
  SandboxInfo,
} from './types.js';

interface ListResponseBody {
  items: SandboxInfo[];
  nextCursor: string | null;
  totalCount: number;
}

/**
 * Entry point:
 *
 *   import { Mogenius } from '@mogenius/sandbox';
 *   const mogenius = new Mogenius();                    // MOGENIUS_API_KEY etc. from the environment
 *   const sandbox = await mogenius.create();            // claimed from the default warm pool, started
 *   const result = await sandbox.process.executeCommand('echo hello');
 *   await mogenius.delete(sandbox);
 *
 * Sandboxes run as pods in your own cluster, under your RBAC, through the
 * mogenius operator; nothing leaves the cluster but the API calls.
 */
export class Mogenius {
  private readonly api: ApiClient;
  private readonly namespace: string;

  constructor(config: MogeniusConfig = {}) {
    const resolved = resolveConfig(config);
    this.api = new ApiClient(resolved);
    this.namespace = resolved.namespace;
  }

  /**
   * Creates a sandbox and, by default, waits until it is started. Without
   * `image` it is claimed from the profile's warm pool and ready in seconds;
   * with `image` a pod is started from the profile's template with that image.
   */
  async create(params: CreateSandboxParams = {}, options: CreateSandboxOptions = {}): Promise<Sandbox> {
    const timeout = options.timeout ?? 60;
    const info = await this.api.post<SandboxInfo>(`/sandbox/${encodeURIComponent(this.namespace)}`, {
      ...(params.name ? { name: params.name } : {}),
      ...(params.snapshot ? { profile: params.snapshot } : {}),
      ...('image' in params && params.image ? { image: params.image } : {}),
      ...(params.labels ? { labels: params.labels } : {}),
      ...(params.envVars && Object.keys(params.envVars).length > 0 ? { env: params.envVars } : {}),
      ...(params.autoDeleteInterval !== undefined && params.autoDeleteInterval > 0
        ? { ttlMinutes: Math.ceil(params.autoDeleteInterval) }
        : {}),
      ...(params.ephemeral !== undefined ? { ephemeral: params.ephemeral } : {}),
      waitForStart: timeout > 0,
      ...(timeout > 0 ? { waitTimeoutSeconds: Math.min(300, Math.ceil(timeout)) } : {}),
    });
    const sandbox = new Sandbox(this.api, info, params.language ?? 'python');
    if (timeout > 0 && sandbox.state !== 'started') {
      // The platform waited as long as it may; finish the wait here with what is left.
      await sandbox.waitUntilStarted(Math.max(1, timeout - 300));
    }
    return sandbox;
  }

  /** The sandbox with that id (claim or sandbox name). */
  async get(sandboxId: string, language: CodeLanguage = 'python'): Promise<Sandbox> {
    const info = await this.api.get<SandboxInfo>(
      `/sandbox/${encodeURIComponent(this.namespace)}/${encodeURIComponent(sandboxId)}`,
    );
    return new Sandbox(this.api, info, language);
  }

  /** The first sandbox matching the filter; by id when given, else by labels and state. */
  async findOne(filter: SandboxFilter): Promise<Sandbox> {
    if (filter.id) {
      return this.get(filter.id);
    }
    const page = await this.list(filter.labels, { limit: 1 }, filter.state);
    const first = page.items[0];
    if (!first) {
      throw new MogeniusNotFoundError(
        `No sandbox matches ${JSON.stringify(filter)} in namespace "${this.namespace}".`,
        {
          errorCode: 'SANDBOX_NOT_FOUND',
          source: 'sdk',
        },
      );
    }
    return first;
  }

  /** Sandboxes in the namespace, optionally filtered by labels and state, one page at a time. */
  async list(
    labels?: Record<string, string>,
    options: ListSandboxesOptions = {},
    state?: SandboxFilter['state'],
  ): Promise<PaginatedSandboxes<Sandbox>> {
    const body = await this.api.get<ListResponseBody>(`/sandbox/${encodeURIComponent(this.namespace)}`, {
      labels:
        labels && Object.keys(labels).length > 0
          ? Object.entries(labels)
              .map(([key, value]) => `${key}=${value}`)
              .join(',')
          : undefined,
      state,
      limit: options.limit,
      cursor: options.cursor,
    });
    return {
      items: body.items.map((info) => new Sandbox(this.api, info)),
      nextCursor: body.nextCursor,
      total: body.totalCount,
    };
  }

  /** Starts a stopped sandbox. */
  async start(sandbox: Sandbox, timeout?: number): Promise<void> {
    await sandbox.start(timeout);
  }

  /** Stops a running sandbox; the volume stays. */
  async stop(sandbox: Sandbox, timeout?: number): Promise<void> {
    await sandbox.stop(timeout);
  }

  /** Deletes a sandbox. */
  async delete(sandbox: Sandbox): Promise<void> {
    await sandbox.delete();
  }

  /** Snapshot service. Profiles are managed on the cluster's Sandboxes page for now (MOG-4696). */
  get snapshot(): never {
    throw new MogeniusUnsupportedError(
      'Managing snapshots from the SDK',
      'Use the profile name as `snapshot` in create(); profiles are configured on the Sandboxes page.',
    );
  }

  /** Volume service. Each sandbox has its own volume from the profile. */
  get volume(): never {
    throw new MogeniusUnsupportedError(
      'Shared volumes',
      'Each sandbox gets its own persistent volume from the profile.',
    );
  }
}
