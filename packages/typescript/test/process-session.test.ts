import { describe, expect, it } from 'vitest';
import { Mogenius, MogeniusConflictError, MogeniusNotFoundError } from '../src/index.js';
import { CONFIG, FakeWebSocket, fakeFetch, sandboxInfo, streamBytes } from './helpers.js';

const sessionBody = (overrides: Record<string, unknown> = {}) => ({
  sessionId: 'dev',
  namespace: 'agent-sandbox',
  podName: 'default-k27tp',
  container: 'sandbox',
  tty: false,
  createdAt: '2026-10-06T12:00:00.000Z',
  lastUsedAt: '2026-10-06T12:00:05.000Z',
  commands: [],
  ...overrides,
});

// sessions live on the pod routes, addressed by the sandbox's pod, not the claim
const base = '/resource/session/agent-sandbox/default-k27tp';

describe('Process sessions', () => {
  it('creates a session and reads it back in the SDK shape', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      {
        status: 201,
        body: sessionBody({ commands: [{ id: 'c1', command: 'true', exitCode: 0, startedAt: 'x', finishedAt: 'y' }] }),
      },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const session = await sandbox.process.createSession('dev');

    expect(calls[1]!.method).toBe('POST');
    expect(calls[1]!.url.pathname).toBe(base);
    expect(calls[1]!.body).toEqual({ sessionId: 'dev', container: 'sandbox' });
    expect(session).toEqual({
      sessionId: 'dev',
      container: 'sandbox',
      createdAt: '2026-10-06T12:00:00.000Z',
      lastUsedAt: '2026-10-06T12:00:05.000Z',
      commands: [{ id: 'c1', command: 'true', exitCode: 0 }],
    });
  });

  it('names the container when asked, lists, gets and deletes', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: sessionBody() },
      { body: [sessionBody(), sessionBody({ sessionId: 'other' })] },
      { body: sessionBody() },
      { status: 204 },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    await sandbox.process.createSession('dev', 'sidecar');
    expect(calls[1]!.body).toEqual({ sessionId: 'dev', container: 'sidecar' });

    const list = await sandbox.process.listSessions();
    expect(calls[2]!.method).toBe('GET');
    expect(calls[2]!.url.pathname).toBe(base);
    expect(list.map((s) => s.sessionId)).toEqual(['dev', 'other']);

    await sandbox.process.getSession('dev');
    expect(calls[3]!.url.pathname).toBe(`${base}/dev`);

    await sandbox.process.deleteSession('dev');
    expect(calls[4]!.method).toBe('DELETE');
    expect(calls[4]!.url.pathname).toBe(`${base}/dev`);
  });

  it('runs a command synchronously and leaves the exit code out while it still runs', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: { cmdId: 'c1', exitCode: 0, output: 'bar /tmp\n' } },
      { status: 201, body: { cmdId: 'c2', exitCode: null, output: 'started\n' } },
      { status: 201, body: { cmdId: 'c3', exitCode: null } },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const done = await sandbox.process.executeSessionCommand('dev', { command: 'echo $FOO $PWD', timeout: 30 });
    expect(calls[1]!.url.pathname).toBe(`${base}/dev/exec`);
    expect(calls[1]!.body).toEqual({ command: 'echo $FOO $PWD', timeout: 30 });
    expect(done).toEqual({ cmdId: 'c1', exitCode: 0, output: 'bar /tmp\n' });

    const pending = await sandbox.process.executeSessionCommand('dev', { command: 'sleep 90' });
    expect(calls[2]!.body).toEqual({ command: 'sleep 90' });
    expect(pending).toEqual({ cmdId: 'c2', output: 'started\n' });

    const background = await sandbox.process.executeSessionCommand('dev', { command: 'npm test', runAsync: true });
    expect(calls[3]!.body).toEqual({ command: 'npm test', runAsync: true });
    expect(background).toEqual({ cmdId: 'c3' });
  });

  it('reads a command, its logs so far and sends input', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: { id: 'c1', command: 'read x', exitCode: null, startedAt: 'x', finishedAt: null } },
      { body: { output: 'prompt: ', stdout: 'prompt: ', stderr: '', exitCode: null, truncated: false } },
      { status: 204 },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const command = await sandbox.process.getSessionCommand('dev', 'c1');
    expect(calls[1]!.url.pathname).toBe(`${base}/dev/command/c1`);
    expect(command).toEqual({ id: 'c1', command: 'read x' });

    const logs = await sandbox.process.getSessionCommandLogs('dev', 'c1');
    expect(calls[2]!.url.pathname).toBe(`${base}/dev/command/c1/logs`);
    expect(logs).toBe('prompt: ');

    await sandbox.process.sendSessionCommandInput('dev', 'c1', 'hello\n');
    expect(calls[3]!.method).toBe('POST');
    expect(calls[3]!.url.pathname).toBe(`${base}/dev/command/c1/input`);
    expect(calls[3]!.body).toEqual({ data: 'hello\n' });
  });

  it('follows the logs live over the session-log stream and resolves at the exit', async () => {
    FakeWebSocket.instances = [];
    const { fetch } = fakeFetch({ body: sandboxInfo() });
    const sandbox = await new Mogenius({
      ...CONFIG,
      fetch,
      streamUrl: 'wss://stream.test',
      webSocket: FakeWebSocket as unknown as typeof WebSocket,
    }).get('default-abc12');

    const chunks: string[] = [];
    const following = sandbox.process.getSessionCommandLogs('dev', 'c1', (chunk) => chunks.push(chunk));
    await Promise.resolve();
    const socket = FakeWebSocket.instances[0]!;
    const params = socket.url.searchParams;
    expect(params.get('type')).toBe('CLUSTER__POD_SESSION_LOG');
    expect(params.get('cmd')).toBe('session-log');
    expect(params.get('podName')).toBe('default-k27tp');
    expect(params.get('sessionId')).toBe('dev');
    expect(params.get('cmdId')).toBe('c1');
    expect(params.has('command')).toBe(false);

    socket.frame('PEER_IS_READY');
    socket.frame(streamBytes(0, '1\n'));
    socket.frame(streamBytes(1, 'warn\n'));
    socket.frame(streamBytes(0, '2\n'));
    socket.frame('EXIT:0');
    socket.end(1000, 'Peer closed the connection.');

    await following;
    expect(chunks.join('')).toBe('1\nwarn\n2\n');
  });

  it('refuses sessions of a sandbox that still has no pod after reading it again', async () => {
    const { fetch, calls } = fakeFetch({ body: sandboxInfo({ state: 'stopped', podName: null }) });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    await expect(sandbox.process.createSession('dev')).rejects.toBeInstanceOf(MogeniusConflictError);
    await expect(sandbox.process.listSessions()).rejects.toBeInstanceOf(MogeniusConflictError);
    // only the sandbox itself was read, no session route was asked
    expect(calls.every((call) => call.url.pathname === '/sandbox/agent-sandbox/default-abc12')).toBe(true);
  });

  it('maps session error codes onto the error classes', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      {
        status: 404,
        body: { statusCode: 404, errorCode: 'SESSION_NOT_FOUND', source: 'operator', message: 'session not found' },
      },
      {
        status: 409,
        body: {
          statusCode: 409,
          errorCode: 'SESSION_BUSY',
          source: 'operator',
          message: 'a command is still running in the session',
        },
      },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.process.getSession('nope')).rejects.toBeInstanceOf(MogeniusNotFoundError);
    await expect(sandbox.process.executeSessionCommand('dev', { command: 'true' })).rejects.toBeInstanceOf(
      MogeniusConflictError,
    );
  });

  it('maps a refused follow of an unknown session to the not-found error', async () => {
    FakeWebSocket.instances = [];
    const { fetch } = fakeFetch({ body: sandboxInfo() });
    const sandbox = await new Mogenius({
      ...CONFIG,
      fetch,
      webSocket: FakeWebSocket as unknown as typeof WebSocket,
    }).get('default-abc12');
    const following = sandbox.process.getSessionCommandLogs('nope', 'c1', () => undefined);
    await Promise.resolve();
    FakeWebSocket.instances[0]!.end(1011, 'session not found');
    await expect(following).rejects.toBeInstanceOf(MogeniusNotFoundError);
  });
});
