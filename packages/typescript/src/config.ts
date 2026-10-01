import { MogeniusError } from './errors.js';
import type { MogeniusConfig, ResolvedConfig } from './types.js';

export const DEFAULT_API_URL = 'https://platform-api.mogenius.com';
export const DEFAULT_NAMESPACE = 'agent-sandbox';

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
    throw new MogeniusError('No fetch available: use Node 20+ or pass `fetch` in the config.');
  }
  return {
    apiKey,
    apiUrl,
    organizationId: config.organizationId ?? env('MOGENIUS_ORGANIZATION_ID'),
    clusterId: config.clusterId ?? env('MOGENIUS_CLUSTER_ID'),
    namespace: config.namespace ?? config.target ?? env('MOGENIUS_SANDBOX_NAMESPACE') ?? DEFAULT_NAMESPACE,
    workspaceName: config.workspaceName ?? env('MOGENIUS_WORKSPACE_NAME'),
    fetch: fetchImpl,
  };
}
