import { connect, type Socket } from 'node:net';
import { afterEach, describe, expect, it } from 'vitest';
import {
  Mogenius,
  MogeniusConflictError,
  MogeniusError,
  MogeniusForbiddenError,
  MogeniusNotFoundError,
  MogeniusValidationError,
} from '../src/index.js';
import type { Tunnel } from '../src/index.js';
import { tunnelTiming } from '../src/tunnel.js';
import { CONFIG, FakeWebSocket, fakeFetch, sandboxInfo } from './helpers.js';

/** A data frame of the port-forward stream: id length, id, bytes. */
const frame = (id: string, text: string): Uint8Array =>
  new Uint8Array([id.length, ...Buffer.from(id), ...Buffer.from(text)]);
const sent = (socket: FakeWebSocket): (string | Uint8Array)[] => socket.sent;

const defaultDelays = [...tunnelTiming.reconnectDelaysMs];
const open: Tunnel[] = [];

afterEach(async () => {
  await Promise.all(open.splice(0).map((tunnel) => tunnel.close()));
  tunnelTiming.reconnectDelaysMs = [...defaultDelays];
});

/** Polls until `check` holds: tunnel, gateway and local sockets talk asynchronously. */
async function until(check: () => boolean, what: string): Promise<void> {
  for (let attempt = 0; attempt < 400; attempt++) {
    if (check()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

async function sandbox(overrides: Parameters<typeof sandboxInfo>[0] = {}) {
  FakeWebSocket.instances = [];
  const { fetch } = fakeFetch({ body: sandboxInfo(overrides) });
  return new Mogenius({
    ...CONFIG,
    fetch,
    streamUrl: 'wss://stream.test',
    webSocket: FakeWebSocket as unknown as typeof WebSocket,
  }).get('default-abc12');
}

/** Opens a tunnel, playing the gateway that answers ready. */
async function openTunnel(port = 8080): Promise<{ tunnel: Tunnel; socket: FakeWebSocket }> {
  const pending = (await sandbox()).tunnel(port);
  await until(() => FakeWebSocket.instances.length === 1, 'the gateway stream');
  const socket = FakeWebSocket.instances[0]!;
  socket.frame('PEER_IS_READY');
  const tunnel = await pending;
  open.push(tunnel);
  return { tunnel, socket };
}

async function connectTo(tunnel: Tunnel): Promise<Socket> {
  const client = connect(tunnel.port, tunnel.host);
  await new Promise<void>((resolve, reject) => {
    client.once('connect', resolve);
    client.once('error', reject);
  });
  return client;
}

describe('Sandbox.tunnel', () => {
  it('opens the pod port through the gateway and forwards bytes both ways', async () => {
    const { tunnel, socket } = await openTunnel(8080);

    expect(Object.fromEntries(socket.url.searchParams)).toMatchObject({
      authorization: 'bearer mo_pat:user:secret',
      organizationId: 'org-1',
      clusterId: 'cluster-1',
      type: 'PORT_FORWARD',
      cmd: 'port-forward',
      namespace: 'agent-sandbox',
      kind: 'Pod',
      workloadName: 'default-k27tp',
      remotePort: '8080',
    });
    expect(socket.url.searchParams.has('binary')).toBe(false);
    expect(sent(socket)[0]).toBe('PEER_IS_READY');
    expect(tunnel.url).toBe(`http://127.0.0.1:${tunnel.port}`);

    const client = await connectTo(tunnel);
    await until(() => sent(socket).includes('PFM:O:c1'), 'PFM:O:c1');
    client.write('hello');
    await until(() => sent(socket).some((data) => data instanceof Uint8Array), 'a data frame');
    const data = sent(socket).find((item) => item instanceof Uint8Array) as Uint8Array;
    expect(Buffer.from(data).toString()).toBe('\x02c1hello');

    const received = new Promise<string>((resolve) => client.once('data', (chunk) => resolve(chunk.toString())));
    socket.frame(frame('c1', 'HELLO'));
    expect(await received).toBe('HELLO');

    client.end();
    await until(() => sent(socket).includes('PFM:C:c1'), 'PFM:C:c1');
  });

  it('ends a connection the pod closed, after what it sent', async () => {
    const { tunnel, socket } = await openTunnel();
    const client = await connectTo(tunnel);
    await until(() => sent(socket).includes('PFM:O:c1'), 'PFM:O:c1');
    let received = '';
    client.on('data', (chunk) => (received += chunk.toString()));
    const closed = new Promise<void>((resolve) => client.once('close', () => resolve()));

    socket.frame(frame('c1', 'bye'));
    socket.frame('PFM:C:c1');

    await closed;
    expect(received).toBe('bye');
  });

  it('says why it cannot open', async () => {
    const cases: [(socket: FakeWebSocket) => void, new (...args: never[]) => Error, RegExp][] = [
      [(socket) => socket.end(1008, 'POD_DOES_NOT_EXIST'), MogeniusNotFoundError, /does not exist/],
      [(socket) => socket.frame('ERROR: port 8080 is not open'), MogeniusError, /8080/],
      [(socket) => socket.frame('UNAUTHORIZED'), MogeniusForbiddenError, /UNAUTHORIZED/],
      [(socket) => socket.end(1000), MogeniusError, /before the operator opened it/],
    ];
    for (const [play, kind, message] of cases) {
      const pending = (await sandbox()).tunnel(8080);
      await until(() => FakeWebSocket.instances.length === 1, 'the gateway stream');
      play(FakeWebSocket.instances[0]!);
      const error = await pending.then(
        () => undefined,
        (err: unknown) => err,
      );
      expect(error).toBeInstanceOf(kind);
      expect((error as Error).message).toMatch(message);
    }
  });

  it('reconnects when the stream breaks', async () => {
    tunnelTiming.reconnectDelaysMs = [1];
    const { tunnel, socket } = await openTunnel();
    const before = await connectTo(tunnel);
    await until(() => sent(socket).includes('PFM:O:c1'), 'PFM:O:c1');
    const dropped = new Promise<void>((resolve) => before.once('close', () => resolve()));

    socket.end(1011, 'operator restarted');
    await dropped;
    await until(() => FakeWebSocket.instances.length === 2, 'a second gateway stream');
    const again = FakeWebSocket.instances[1]!;
    again.frame('PEER_IS_READY');
    await until(() => sent(again).includes('PEER_IS_READY'), 'the ready answer');

    await connectTo(tunnel);
    await until(() => sent(again).includes('PFM:O:c2'), 'PFM:O:c2 on the new stream');
  });

  it('ends when the pod is gone', async () => {
    tunnelTiming.reconnectDelaysMs = [1];
    const { tunnel, socket } = await openTunnel();

    socket.end(1011);
    await until(() => FakeWebSocket.instances.length === 2, 'a second gateway stream');
    FakeWebSocket.instances[1]!.end(1008, 'POD_DOES_NOT_EXIST');

    expect(await tunnel.done).toBeInstanceOf(MogeniusNotFoundError);
  });

  it('close() ends the stream and the listener', async () => {
    const { tunnel, socket } = await openTunnel();
    await tunnel.close();

    expect(socket.closedWith).toBe(1000);
    expect(await tunnel.done).toBeUndefined();
    await expect(connectTo(tunnel)).rejects.toThrow();
  });

  it('reads the sandbox again when the handle has no pod yet', async () => {
    FakeWebSocket.instances = [];
    // created without waiting: the handle has no pod, the platform has one by now
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo({ podName: null, state: 'starting' }) },
      { body: sandboxInfo() },
    );
    const box = await new Mogenius({
      ...CONFIG,
      fetch,
      streamUrl: 'wss://stream.test',
      webSocket: FakeWebSocket as unknown as typeof WebSocket,
    }).get('default-abc12');

    const pending = box.tunnel(8080);
    await until(() => FakeWebSocket.instances.length === 1, 'the gateway stream');
    FakeWebSocket.instances[0]!.frame('PEER_IS_READY');
    open.push(await pending);

    expect(calls).toHaveLength(2);
    expect(FakeWebSocket.instances[0]!.url.searchParams.get('workloadName')).toBe('default-k27tp');
  });

  it('needs a TCP port and a pod', async () => {
    await expect((await sandbox()).tunnel(0)).rejects.toBeInstanceOf(MogeniusValidationError);
    await expect((await sandbox({ podName: null })).tunnel(8080)).rejects.toBeInstanceOf(MogeniusConflictError);
    expect(FakeWebSocket.instances).toHaveLength(0);
  });
});
