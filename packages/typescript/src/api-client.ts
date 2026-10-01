import { errorFromResponse, MogeniusError } from './errors.js';
import type { ErrorBody, ResolvedConfig } from './types.js';

/**
 * The HTTP layer under the SDK: one place for the platform's headers, JSON
 * handling and the error shape. Routes are the platform's `/sandbox/...`
 * endpoints; see the generated OpenAPI clients once MOG-4698/4699 land.
 */
export class ApiClient {
  constructor(private readonly config: ResolvedConfig) {}

  get namespace(): string {
    return this.config.namespace;
  }

  async get<T>(path: string, query?: Record<string, string | number | undefined>): Promise<T> {
    return this.request<T>('GET', path, query);
  }

  async post<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>('POST', path, undefined, body);
  }

  async patch<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>('PATCH', path, undefined, body);
  }

  async delete<T>(path: string): Promise<T> {
    return this.request<T>('DELETE', path);
  }

  private headers(hasBody: boolean): Record<string, string> {
    const headers: Record<string, string> = {
      authorization: `Bearer ${this.config.apiKey}`,
      accept: 'application/json',
      'user-agent': '@mogenius/sandbox',
    };
    if (this.config.organizationId) {
      headers['organization-id'] = this.config.organizationId;
    }
    if (this.config.clusterId) {
      headers['cluster-id'] = this.config.clusterId;
    }
    if (this.config.workspaceName) {
      headers['workspace-name'] = this.config.workspaceName;
    }
    if (hasBody) {
      headers['content-type'] = 'application/json';
    }
    return headers;
  }

  private async request<T>(
    method: string,
    path: string,
    query?: Record<string, string | number | undefined>,
    body?: unknown,
  ): Promise<T> {
    const url = new URL(`${this.config.apiUrl}${path}`);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined && value !== '') {
        url.searchParams.set(key, String(value));
      }
    }

    let response: Response;
    try {
      response = await this.config.fetch(url, {
        method,
        headers: this.headers(body !== undefined),
        body: body !== undefined ? JSON.stringify(body) : undefined,
      });
    } catch (err) {
      throw new MogeniusError(`${method} ${url.pathname} failed: ${(err as Error).message}`, { source: 'sdk' });
    }

    const text = await response.text();
    const parsed = parseJson(text);
    if (!response.ok) {
      throw errorFromResponse(
        response.status,
        parsed as ErrorBody | null,
        `${method} ${url.pathname} → HTTP ${response.status}`,
      );
    }
    return parsed as T;
  }
}

function parseJson(text: string): unknown {
  if (!text) {
    return null;
  }
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
}
