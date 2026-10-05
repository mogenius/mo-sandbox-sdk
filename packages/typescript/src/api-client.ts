import { errorFromResponse, MogeniusError } from './errors.js';
import type { ErrorBody, ResolvedConfig } from './types.js';

type Query = Record<string, string | number | undefined>;

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

  async get<T>(path: string, query?: Query): Promise<T> {
    return this.request<T>('GET', path, query);
  }

  /** GET whose body is bytes, not JSON. */
  async getBinary(path: string, query?: Query): Promise<Buffer> {
    const response = await this.send('GET', path, query, undefined);
    if (!response.ok) {
      const parsed = parseJson(await response.text());
      throw errorFromResponse(response.status, parsed as ErrorBody | null, `GET ${path} → HTTP ${response.status}`);
    }
    return Buffer.from(await response.arrayBuffer());
  }

  /**
   * GET of an absolute URL the platform handed out (a one-time download
   * link). No auth headers: the token in the URL is the credential, the way a
   * browser opens the same link. The body comes back as a stream.
   */
  async fetchStream(url: string): Promise<ReadableStream<Uint8Array>> {
    let response: Response;
    try {
      response = await this.config.fetch(url, {
        method: 'GET',
        headers: { accept: '*/*', 'user-agent': '@mogenius/sandbox' },
      });
    } catch (err) {
      throw new MogeniusError(`GET ${url} failed: ${(err as Error).message}`, { source: 'sdk' });
    }
    if (!response.ok) {
      const parsed = parseJson(await response.text());
      throw errorFromResponse(
        response.status,
        parsed as ErrorBody | null,
        `GET download link → HTTP ${response.status}`,
      );
    }
    if (!response.body) {
      throw new MogeniusError('The download link answered without a body.', { source: 'api' });
    }
    return response.body;
  }

  async post<T>(path: string, body?: unknown, query?: Query): Promise<T> {
    return this.request<T>('POST', path, query, body);
  }

  /** Multipart POST; the form carries its own content type and boundary. */
  async postForm<T>(path: string, form: FormData, query?: Query): Promise<T> {
    return this.request<T>('POST', path, query, form);
  }

  async patch<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>('PATCH', path, undefined, body);
  }

  async delete<T>(path: string, query?: Query): Promise<T> {
    return this.request<T>('DELETE', path, query);
  }

  private headers(body: unknown): Record<string, string> {
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
    // a FormData body sets its own multipart content type with the boundary
    if (body !== undefined && !(body instanceof FormData)) {
      headers['content-type'] = 'application/json';
    }
    return headers;
  }

  private async request<T>(method: string, path: string, query?: Query, body?: unknown): Promise<T> {
    const response = await this.send(method, path, query, body);
    const parsed = parseJson(await response.text());
    if (!response.ok) {
      throw errorFromResponse(
        response.status,
        parsed as ErrorBody | null,
        `${method} ${path} → HTTP ${response.status}`,
      );
    }
    return parsed as T;
  }

  private async send(method: string, path: string, query?: Query, body?: unknown): Promise<Response> {
    const url = new URL(`${this.config.apiUrl}${path}`);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined && value !== '') {
        url.searchParams.set(key, String(value));
      }
    }
    try {
      return await this.config.fetch(url, {
        method,
        headers: this.headers(body),
        body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
      });
    } catch (err) {
      throw new MogeniusError(`${method} ${url.pathname} failed: ${(err as Error).message}`, { source: 'sdk' });
    }
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
