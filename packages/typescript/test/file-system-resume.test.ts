import { describe, expect, it, vi } from 'vitest';
import { Mogenius, MogeniusError } from '../src/index.js';
import { CONFIG, sandboxInfo } from './helpers.js';

const LINK = 'https://api.test/storage/download/tok-r';
const ETAG = '"10-1700000000"';

/** A body that hands out `parts` and then either ends or breaks like a dropped socket. */
function body(parts: string[], breakAtEnd: boolean): ReadableStream<Uint8Array> {
  const queue = parts.map((p) => new TextEncoder().encode(p));
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      const next = queue.shift();
      if (next) {
        controller.enqueue(next);
      } else if (breakAtEnd) {
        controller.error(new TypeError('terminated'));
      } else {
        controller.close();
      }
    },
  });
}

interface Seen {
  url: string;
  headers: Record<string, string>;
}

/** Sandbox lookup, the link, then the given download answers in order. */
function server(...downloads: (() => Response)[]) {
  const seen: Seen[] = [];
  const queue = [...downloads];
  const fetchImpl = vi.fn((input: URL | string | Request, init?: RequestInit): Promise<Response> => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes('/download-link')) {
      return Promise.resolve(new Response(JSON.stringify({ url: LINK, expiresInSeconds: 900 }), { status: 200 }));
    }
    if (url === LINK) {
      seen.push({ url, headers: { ...((init?.headers as Record<string, string>) ?? {}) } });
      return Promise.resolve(queue.shift()!());
    }
    return Promise.resolve(new Response(JSON.stringify(sandboxInfo()), { status: 200 }));
  });
  return { fetch: fetchImpl as unknown as typeof fetch, seen };
}

const full =
  (parts: string[], breakAtEnd: boolean, headers: Record<string, string> = {}) =>
  () =>
    new Response(body(parts, breakAtEnd), {
      status: 200,
      headers: { 'content-length': '10', 'accept-ranges': 'bytes', etag: ETAG, ...headers },
    });
const partial =
  (from: number, parts: string[], breakAtEnd = false) =>
  () =>
    new Response(body(parts, breakAtEnd), {
      status: 206,
      headers: { 'content-range': `bytes ${from}-9/10`, 'content-length': String(10 - from), etag: ETAG },
    });

async function download(fetchImpl: typeof fetch): Promise<string> {
  const sandbox = await new Mogenius({ ...CONFIG, fetch: fetchImpl }).get('default-abc12');
  // no pauses between attempts in tests
  (sandbox.fs as unknown as { resumeDelaysMs: number[] }).resumeDelaysMs = [0, 0, 0, 0, 0];
  return (await sandbox.fs.downloadFile('/data/big.bin')).toString('utf8');
}

describe('FileSystem download resume (MOG-4747)', () => {
  it('continues after a dropped connection with Range and If-Range', async () => {
    const { fetch, seen } = server(full(['hel', 'lo'], true), partial(5, ['wor', 'ld']));
    expect(await download(fetch)).toBe('helloworld');
    expect(seen).toHaveLength(2);
    expect(seen[0]!.headers.range).toBeUndefined();
    expect(seen[1]!.headers).toMatchObject({ range: 'bytes=5-', 'if-range': ETAG });
    expect(seen[1]!.headers.authorization).toBeUndefined();
  });

  it('continues when the body ends before the announced length', async () => {
    const { fetch, seen } = server(full(['hello'], false), partial(5, ['world']));
    expect(await download(fetch)).toBe('helloworld');
    expect(seen[1]!.headers.range).toBe('bytes=5-');
  });

  it('survives several drops in a row', async () => {
    const { fetch } = server(
      full(['he'], true),
      partial(2, ['llo'], true),
      partial(5, ['w'], true),
      partial(6, ['orld']),
    );
    expect(await download(fetch)).toBe('helloworld');
  });

  it('fails instead of repeating bytes when the resume comes back as 200 (file changed)', async () => {
    const { fetch } = server(full(['hello'], true), full(['HELLOWORLD'], false));
    await expect(download(fetch)).rejects.toThrow(/could not continue at byte 5/);
  });

  it('fails when the 206 starts somewhere else', async () => {
    const { fetch } = server(full(['hello'], true), partial(3, ['loworld']));
    await expect(download(fetch)).rejects.toBeInstanceOf(MogeniusError);
  });

  it('gives up after five attempts', async () => {
    const { fetch, seen } = server(
      full(['hello'], true),
      partial(5, [], true),
      partial(5, [], true),
      partial(5, [], true),
      partial(5, [], true),
      partial(5, [], true),
    );
    await expect(download(fetch)).rejects.toThrow(/stopped at byte 5 of 10 and could not be resumed/);
    expect(seen).toHaveLength(6);
  });

  it('does not try to resume a folder or anything without Accept-Ranges', async () => {
    const { fetch, seen } = server(full(['hello'], true, { 'accept-ranges': 'none', etag: '' }));
    await expect(download(fetch)).rejects.toThrow(/stopped at byte 5/);
    expect(seen).toHaveLength(1);
  });

  it('passes a complete download through untouched', async () => {
    const { fetch, seen } = server(full(['hello', 'world'], false));
    expect(await download(fetch)).toBe('helloworld');
    expect(seen).toHaveLength(1);
  });
});
