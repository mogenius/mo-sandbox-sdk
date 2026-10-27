import { MogeniusError, MogeniusValidationError } from './errors.js';
import type { MogeniusConfig, ResolvedConfig } from './types.js';

export const DEFAULT_API_URL = 'https://platform-api.mogenius.com';
export const DEFAULT_STREAM_URL = 'wss://k8s-cmd-stream.mogenius.com';
export const DEFAULT_NAMESPACE = 'agent-sandbox';
export const DEFAULT_PROFILE = 'default';

const env = (name: string): string | undefined => {
  const value = typeof process !== 'undefined' ? process.env?.[name] : undefined;
  return value && value.trim() !== '' ? value.trim() : undefined;
};

/**
 * Explicit config wins, environment variables fill the gaps. Only the key is
 * required: an API key with a single organization and cluster scope needs no
 * ids, the platform picks them from the key.
 */
export function resolveConfig(config: MogeniusConfig = {}): ResolvedConfig {
  const apiKey = config.apiKey ?? env('MOGENIUS_API_KEY');
  if (!apiKey) {
    throw new MogeniusError(
      'No API key: pass `apiKey` to `new Mogenius({...})` or set MOGENIUS_API_KEY. Create one under Organization → API keys.',
    );
  }
  const apiUrl = (config.apiUrl ?? env('MOGENIUS_API_URL') ?? DEFAULT_API_URL).replace(/\/+$/, '');
  const fetchImpl = config.fetch ?? globalThis.fetch;
  if (typeof fetchImpl !== 'function') {
    throw new MogeniusError('No fetch available: use Node 24+ or pass `fetch` in the config.');
  }
  const fileTransport = (config.fileTransport ?? env('MOGENIUS_FILE_TRANSPORT') ?? 'http').toLowerCase();
  if (fileTransport !== 'http' && fileTransport !== 'sftp') {
    throw new MogeniusValidationError(
      `fileTransport (MOGENIUS_FILE_TRANSPORT) is "${fileTransport}"; it is http or sftp.`,
    );
  }
  return {
    apiKey,
    apiUrl,
    streamUrl: (config.streamUrl ?? env('MOGENIUS_STREAM_URL') ?? DEFAULT_STREAM_URL).replace(/\/+$/, ''),
    organizationId: config.organizationId ?? env('MOGENIUS_ORGANIZATION_ID'),
    clusterId: config.clusterId ?? env('MOGENIUS_CLUSTER_ID'),
    namespace: config.namespace ?? config.target ?? env('MOGENIUS_SANDBOX_NAMESPACE') ?? DEFAULT_NAMESPACE,
    profile: config.profile ?? env('MOGENIUS_SANDBOX_PROFILE') ?? DEFAULT_PROFILE,
    workspaceName: config.workspaceName ?? env('MOGENIUS_WORKSPACE_NAME'),
    fileTransport,
    fetch: fetchImpl,
    // only needed for streams, so its absence is reported there
    webSocket: config.webSocket ?? globalThis.WebSocket,
  };
}
