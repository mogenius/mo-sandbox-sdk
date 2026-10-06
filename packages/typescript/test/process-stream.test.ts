import { describe, expect, it } from 'vitest';
import { Mogenius, MogeniusConflictError, MogeniusError, MogeniusNotFoundError } from '../src/index.js';
import type { ExecEvent } from '../src/index.js';
import { CONFIG, fakeFetch, sandboxInfo } from './helpers.js';

/**
 * A WebSocket the test drives: it records the URL it was opened with and
 * lets the test play the gateway's frames and close.
 */
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  readyState = 1;
  binaryType = 'blob';
  closedWith: number | undefined;
  onmessage: ((event: { data: string | ArrayBuffer }) => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(readonly url: URL) {
    FakeWebSocket.instances.push(this);
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

const bytes = (tag: number, text: string): Uint8Array => new Uint8Array([tag, ...Buffer.from(text, 'utf8')]);
const text = (data: Uint8Array): string => Buffer.from(data).toString('utf8');

async function openStream(command = 'echo hi', cwd?: string, env?: Record<string, string>, timeout?: number) {
  FakeWebSocket.instances = [];
  const { fetch } = fakeFetch({ body: sandboxInfo() });
  const sandbox = await new Mogenius({
    ...CONFIG,
    fetch,
    streamUrl: 'wss://stream.test',
    webSocket: FakeWebSocket as unknown as typeof WebSocket,
  }).get('default-abc12');
  const stream = sandbox.process.executeCommandStream(command, cwd, env, timeout);
  // the socket opens on the first pull
  const first = stream.next();
  await Promise.resolve();
  const socket = FakeWebSocket.instances[0]!;
  return { stream, first, socket };
}

/** Ends a stream the test only inspected: a clean exit, then the generator is drained. */
async function finish(
  socket: FakeWebSocket,
  stream: AsyncGenerator<ExecEvent, void, void>,
  first: Promise<IteratorResult<ExecEvent>>,
): Promise<void> {
  socket.frame('EXIT:0');
  socket.end(1000);
  await first;
  await stream.return();
}

async function collect(
  stream: AsyncGenerator<ExecEvent, void, void>,
  first: Promise<IteratorResult<ExecEvent>>,
): Promise<ExecEvent[]> {
  const events: ExecEvent[] = [];
  const head = await first;
  if (!head.done) {
    events.push(head.value);
  }
  for await (const event of stream) {
    events.push(event);
  }
  return events;
}

describe('Process.executeCommandStream', () => {
  it('opens the gateway socket with the exec request in the query string', async () => {
    const { socket, stream, first } = await openStream('npm test', '/app', { CI: '1' }, 300);
    const params = socket.url.searchParams;
    expect(socket.url.origin + socket.url.pathname).toBe('wss://stream.test/xterm-stream');
    expect(params.get('authorization')).toBe('bearer mo_pat:user:secret');
    expect(params.get('organizationId')).toBe('org-1');
    expect(params.get('clusterId')).toBe('cluster-1');
    expect(params.get('type')).toBe('CLUSTER__POD_EXEC');
    expect(params.get('cmd')).toBe('exec');
    expect(params.get('namespace')).toBe('agent-sandbox');
    expect(params.get('podName')).toBe('default-k27tp');
    expect(params.get('container')).toBe('sandbox');
    expect(params.get('command')).toBe('npm test');
    expect(params.get('cwd')).toBe('/app');
    expect(params.get('env')).toBe('{"CI":"1"}');
    expect(params.get('timeout')).toBe('300');
    expect(params.get('binary')).toBe('1');
    expect(socket.binaryType).toBe('arraybuffer');
    await finish(socket, stream, first);
  });

  it('leaves cwd and env out when absent and defaults the timeout', async () => {
    const { socket, stream, first } = await openStream('true', undefined, {});
    expect(socket.url.searchParams.has('cwd')).toBe(false);
    expect(socket.url.searchParams.has('env')).toBe(false);
    expect(socket.url.searchParams.get('timeout')).toBe('10');
    await finish(socket, stream, first);
  });

  it('yields stdout and stderr apart, then the exit event, then ends', async () => {
    const { socket, stream, first } = await openStream();
    socket.frame('PEER_IS_READY');
    socket.frame(bytes(0, '1\n'));
    socket.frame(bytes(1, 'warn\n'));
    socket.frame(bytes(0, '2\n'));
    socket.frame('EXIT:3');
    socket.end(1000, 'Peer closed the connection.');

    const events = await collect(stream, first);
    expect(events.map((e) => (e.type === 'exit' ? e : { type: e.type, text: text(e.data) }))).toEqual([
      { type: 'stdout', text: '1\n' },
      { type: 'stderr', text: 'warn\n' },
      { type: 'stdout', text: '2\n' },
      { type: 'exit', exitCode: 3, truncated: false, timedOut: false },
    ]);
  });

  it('reports a truncated and a timed-out run on the exit event', async () => {
    const { socket, stream, first } = await openStream();
    socket.frame('TRUNCATED');
    socket.frame('TIMEOUT');
    socket.frame('EXIT:137');
    socket.end(1000);
    const events = await collect(stream, first);
    expect(events).toEqual([{ type: 'exit', exitCode: 137, truncated: true, timedOut: true }]);
  });

  it('turns ERROR frames into an operator error', async () => {
    const { socket, stream, first } = await openStream();
    socket.frame('ERROR:exec: read pod agent-sandbox/default-k27tp: pods "default-k27tp" is forbidden');
    const failure = collect(stream, first);
    await expect(failure).rejects.toBeInstanceOf(MogeniusError);
    await expect(failure).rejects.toMatchObject({ source: 'operator', message: expect.stringContaining('forbidden') });
  });

  it('maps a POD_DOES_NOT_EXIST close to the not-found error', async () => {
    const { socket, stream, first } = await openStream();
    socket.end(1011, 'POD_DOES_NOT_EXIST');
    await expect(collect(stream, first)).rejects.toBeInstanceOf(MogeniusNotFoundError);
  });

  it('explains a failed handshake', async () => {
    const { socket, stream, first } = await openStream();
    socket.onerror?.();
    socket.end(1006);
    await expect(collect(stream, first)).rejects.toThrow(/stream gateway at wss:\/\/stream.test/);
  });

  it('fails when the socket closes before an exit code', async () => {
    const { socket, stream, first } = await openStream();
    socket.frame(bytes(0, 'partial'));
    socket.end(1000);
    await expect(collect(stream, first)).rejects.toThrow(/before the command reported an exit code/);
  });

  it('closes the socket when the consumer leaves the loop early', async () => {
    const { socket, stream, first } = await openStream();
    socket.frame(bytes(0, 'line 1\n'));
    socket.frame(bytes(0, 'line 2\n'));
    const head = await first;
    expect(head.done).toBe(false);
    await stream.return();
    expect(socket.closedWith).toBe(1000);
  });

  it('refuses to stream before the sandbox has a pod', async () => {
    const { fetch } = fakeFetch({ body: sandboxInfo({ podName: null, state: 'starting' }) });
    const sandbox = await new Mogenius({
      ...CONFIG,
      fetch,
      webSocket: FakeWebSocket as unknown as typeof WebSocket,
    }).get('default-abc12');
    await expect(sandbox.process.executeCommandStream('true').next()).rejects.toBeInstanceOf(MogeniusConflictError);
  });

  it('needs organization and cluster ids, which the gateway does not infer from the key', async () => {
    const { fetch } = fakeFetch({ body: sandboxInfo() });
    const sandbox = await new Mogenius({
      apiKey: CONFIG.apiKey,
      apiUrl: CONFIG.apiUrl,
      fetch,
      webSocket: FakeWebSocket as unknown as typeof WebSocket,
    }).get('default-abc12');
    await expect(sandbox.process.executeCommandStream('true').next()).rejects.toThrow(/organizationId/);
  });

  it('reports a missing WebSocket implementation', async () => {
    const { fetch } = fakeFetch({ body: sandboxInfo() });
    const sandbox = await new Mogenius({ ...CONFIG, fetch, webSocket: undefined as unknown as typeof WebSocket }).get(
      'default-abc12',
    );
    const saved = globalThis.WebSocket;
    // @ts-expect-error simulating a runtime without WebSocket
    delete globalThis.WebSocket;
    try {
      const noSocket = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
      await expect(noSocket.process.executeCommandStream('true').next()).rejects.toThrow(/No WebSocket available/);
    } finally {
      globalThis.WebSocket = saved;
    }
    void sandbox;
  });
});
