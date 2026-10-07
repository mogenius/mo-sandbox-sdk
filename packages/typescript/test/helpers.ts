import { vi } from 'vitest';
import type { SandboxInfo } from '../src/types.js';

/** One recorded call of the fake fetch. */
export interface RecordedCall {
  method: string;
  url: URL;
  headers: Record<string, string>;
  /** Parsed JSON body, when the request sent JSON. */
  body: unknown;
  /** The multipart form, when the request sent one. */
  form?: FormData;
}

/** A canned answer: status and JSON body, or `raw` bytes with a `contentType`. */
export type AnswerValue = { status?: number; body?: unknown; raw?: Buffer; contentType?: string };
export type Answer = AnswerValue | ((call: RecordedCall) => AnswerValue);

/**
 * A `fetch` that answers from a queue and records what it was asked. Answers
 * are consumed in order; the last one repeats so polling loops have
 * something to read.
 */
export function fakeFetch(...answers: Answer[]) {
  const calls: RecordedCall[] = [];
  const queue = [...answers];
  const fetchImpl = vi.fn((input: URL | string | Request, init?: RequestInit): Promise<Response> => {
    const url = input instanceof URL ? input : new URL(typeof input === 'string' ? input : input.url);
    const call: RecordedCall = {
      method: init?.method ?? 'GET',
      url,
      headers: Object.fromEntries(Object.entries((init?.headers as Record<string, string>) ?? {})),
      body: typeof init?.body === 'string' ? JSON.parse(init.body) : undefined,
      form: init?.body instanceof FormData ? init.body : undefined,
    };
    calls.push(call);
    const next = queue.length > 1 ? queue.shift()! : queue[0];
    const answer = typeof next === 'function' ? next(call) : (next ?? {});
    const status = answer.status ?? 200;
    if (answer.raw) {
      return Promise.resolve(
        new Response(new Uint8Array(answer.raw), {
          status,
          headers: { 'content-type': answer.contentType ?? 'application/octet-stream' },
        }),
      );
    }
    // a 204/205/304 Response must not carry a body at all
    const body = answer.body === undefined || [204, 205, 304].includes(status) ? null : JSON.stringify(answer.body);
    return Promise.resolve(new Response(body, { status, headers: { 'content-type': 'application/json' } }));
  });
  return { fetch: fetchImpl as unknown as typeof fetch, calls };
}

export function sandboxInfo(overrides: Partial<SandboxInfo> = {}): SandboxInfo {
  return {
    id: 'default-abc12',
    name: 'default-abc12',
    namespace: 'agent-sandbox',
    kind: 'SandboxClaim',
    state: 'started',
    stateReason: null,
    labels: {},
    profile: 'default',
    image: 'ghcr.io/mogenius/agent-sandbox-chart/sandbox-default:1.2.0',
    podName: 'default-k27tp',
    sandboxName: 'default-k27tp',
    containerName: 'sandbox',
    serviceFQDN: 'default-k27tp.agent-sandbox.svc.cluster.local',
    operatingMode: 'Running',
    ephemeral: true,
    expiresAt: null,
    createdAt: '2026-10-01T12:00:00.000Z',
    createdBy: 'jane@example.com',
    ...overrides,
  };
}

export const CONFIG = {
  apiKey: 'mo_pat:user:secret',
  apiUrl: 'https://api.test',
  organizationId: 'org-1',
  clusterId: 'cluster-1',
};

/**
 * A WebSocket the test drives: it records the URL it was opened with and
 * what was sent, and lets the test play the gateway's frames and close.
 */
export class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  readyState = 1;
  binaryType = 'blob';
  closedWith: number | undefined;
  sent: string[] = [];
  onmessage: ((event: { data: string | ArrayBuffer }) => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(readonly url: URL) {
    FakeWebSocket.instances.push(this);
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(code?: number): void {
    this.readyState = 3;
    this.closedWith = code;
  }

  /** The gateway sends a frame. */
  frame(data: string | Uint8Array): void {
    this.onmessage?.({ data: typeof data === 'string' ? data : new Uint8Array(data).buffer });
  }

  /** The gateway closes. */
  end(code: number, reason = ''): void {
    this.readyState = 3;
    this.onclose?.({ code, reason });
  }
}

/** A binary stream frame: the tag byte and the text behind it. */
export const streamBytes = (tag: number, text: string): Uint8Array =>
  new Uint8Array([tag, ...Buffer.from(text, 'utf8')]);
