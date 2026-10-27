import { duplexPair, type Duplex } from 'node:stream';
import { Server, utils, type SFTPWrapper } from 'ssh2';
import { afterEach, beforeAll, describe, expect, it } from 'vitest';
import {
  ERROR_CODE_SFTP_UNAVAILABLE,
  Mogenius,
  MogeniusError,
  MogeniusForbiddenError,
  MogeniusValidationError,
} from '../src/index.js';
import type { MogeniusConfig } from '../src/index.js';
import { sftpTiming } from '../src/sftp.js';
import { CONFIG, FakeWebSocket, fakeFetch, sandboxInfo, type RecordedCall } from './helpers.js';

const { STATUS_CODE } = utils.sftp;
const defaultTiming = { ...sftpTiming };

/**
 * The operator's SSH gateway in memory: login "none" (the stream is
 * authenticated), the sftp subsystem writing into `files`. Writes below /ro/
 * are refused, as a read-only mount would; a write lands only on close, as the
 * gateway stages it.
 */
const files = new Map<string, Buffer>();
const dirs = new Set<string>(['/']);
const users: string[] = [];
let server: Server;

beforeAll(() => {
  const hostKey = utils.generateKeyPairSync('ed25519').private;
  server = new Server({ hostKeys: [hostKey] }, (client) => {
    client.on('authentication', (ctx) => {
      users.push(ctx.username);
      ctx.accept();
    });
    client.on('ready', () => {
      client.on('session', (accept) => {
        accept().on('sftp', (acceptSftp) => serveSftp(acceptSftp()));
      });
    });
    client.on('error', () => undefined);
  });
});

function serveSftp(sftp: SFTPWrapper): void {
  const staged = new Map<string, { path: string; data: Buffer }>();
  let next = 0;
  const attrs = (mode: number, size = 0) => ({ mode, uid: 0, gid: 0, size, atime: 0, mtime: 0 });
  sftp.on('OPEN', (reqid, filename) => {
    if (filename.startsWith('/ro/')) {
      sftp.status(reqid, STATUS_CODE.PERMISSION_DENIED, 'read-only file system');
      return;
    }
    const handle = Buffer.from(String(next++));
    staged.set(handle.toString(), { path: filename, data: Buffer.alloc(0) });
    sftp.handle(reqid, handle);
  });
  sftp.on('WRITE', (reqid, handle, offset, data) => {
    const file = staged.get(handle.toString())!;
    const grown = Buffer.alloc(Math.max(file.data.length, offset + data.length));
    file.data.copy(grown);
    data.copy(grown, offset);
    file.data = grown;
    sftp.status(reqid, STATUS_CODE.OK);
  });
  sftp.on('CLOSE', (reqid, handle) => {
    const file = staged.get(handle.toString());
    staged.delete(handle.toString());
    if (file) {
      files.set(file.path, file.data);
    }
    sftp.status(reqid, STATUS_CODE.OK);
  });
  sftp.on('STAT', (reqid, path) => {
    if (dirs.has(path)) {
      sftp.attrs(reqid, attrs(0o40755));
    } else if (files.has(path)) {
      sftp.attrs(reqid, attrs(0o100644, files.get(path)!.length));
    } else {
      sftp.status(reqid, STATUS_CODE.NO_SUCH_FILE);
    }
  });
  sftp.on('MKDIR', (reqid, path) => {
    dirs.add(path);
    sftp.status(reqid, STATUS_CODE.OK);
  });
}

/** The stream gateway's port-forward in mode=ssh: every connection through it reaches the SSH server above. */
class SshGatewaySocket extends FakeWebSocket {
  static closed = 0;
  private readonly pipes = new Map<string, Duplex>();

  constructor(url: URL) {
    super(url);
    queueMicrotask(() => this.frame('PEER_IS_READY'));
  }

  override send(data: string | Uint8Array): void {
    if (typeof data !== 'string') {
      const head = 1 + data[0]!;
      this.pipes.get(Buffer.from(data.subarray(1, head)).toString())?.write(data.subarray(head));
      return;
    }
    this.sent.push(data);
    if (!data.startsWith('PFM:O:')) {
      return;
    }
    const id = data.slice('PFM:O:'.length);
    const [ours, theirs] = duplexPair();
    this.pipes.set(id, ours);
    ours.on('data', (chunk: Buffer) => {
      if (this.readyState === this.OPEN) {
        this.frame(Buffer.concat([Buffer.from([id.length]), Buffer.from(id), chunk]));
      }
    });
    server.injectSocket(theirs as unknown as Parameters<Server['injectSocket']>[0]);
  }

  override close(code?: number): void {
    if (this.readyState !== this.CLOSED) {
      SshGatewaySocket.closed++;
    }
    super.close(code);
    for (const pipe of this.pipes.values()) {
      pipe.destroy();
    }
  }
}

const execResponse = (stdout: string) => ({
  exitCode: 0,
  result: stdout,
  artifacts: { stdout, stderr: '' },
  truncated: false,
  container: 'sandbox',
  durationMs: 4,
});

/** A sandbox whose stream gateway is `socket`; the fetch answers the sandbox, `pwd` and HTTP uploads. */
async function sandbox(socket: typeof FakeWebSocket, config: MogeniusConfig = {}) {
  FakeWebSocket.instances = [];
  const { fetch, calls } = fakeFetch((call: RecordedCall) => {
    if (call.url.pathname.startsWith('/resource/exec/')) {
      return { status: 201, body: execResponse('/home/sandbox\n') };
    }
    if (call.url.pathname.endsWith('/upload')) {
      return { status: 201, body: [{ path: '/w/h.txt', success: true, error: null }] };
    }
    return { body: sandboxInfo() };
  });
  const sb = await new Mogenius({
    ...CONFIG,
    fetch,
    streamUrl: 'wss://stream.test',
    webSocket: socket as unknown as typeof WebSocket,
    ...config,
  }).get('default-abc12');
  return { sb, calls };
}

async function until(check: () => boolean, what: string): Promise<void> {
  for (let attempt = 0; attempt < 400; attempt++) {
    if (check()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error(`timed out waiting for ${what}`);
}

afterEach(() => {
  Object.assign(sftpTiming, defaultTiming);
  files.clear();
  users.length = 0;
  SshGatewaySocket.closed = 0;
  delete process.env.MOGENIUS_FILE_TRANSPORT;
});

describe('uploads over SFTP', () => {
  it('writes every upload kind through one session on the stream gateway, nothing over HTTP', async () => {
    const { sb, calls } = await sandbox(SshGatewaySocket);
    const script = 'jq -n --arg p "$p" \'{password:$p}\' | curl -s -X POST https://example.com -d @-\n';
    const progress: number[] = [];

    await sb.fs.uploadFile(Buffer.from('hello'), '/w/data/a.txt', { transport: 'sftp' });
    await sb.fs.uploadFile(script, '/w/data/run.sh', { transport: 'sftp' });
    await sb.fs.uploadFile('relative', 'notes/r.txt', { transport: 'sftp' });
    const chunks = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(Buffer.from('chunk-1 '));
        controller.enqueue(Buffer.from('chunk-2'));
        controller.close();
      },
    });
    await sb.fs.uploadFileStream(chunks, '/w/s.txt', {
      transport: 'sftp',
      onProgress: (p) => progress.push(p.bytesSent),
    });
    const results = await sb.fs.uploadFiles(
      [
        { source: Buffer.from('one'), destination: '/w/many/1.txt' },
        { source: Buffer.from('denied'), destination: '/ro/2.txt' },
        { source: Buffer.from('three'), destination: '/w/many/3.txt' },
      ],
      { transport: 'sftp' },
    );

    expect(Object.fromEntries([...files].map(([path, data]) => [path, data.toString()]))).toEqual({
      '/w/data/a.txt': 'hello',
      '/w/data/run.sh': script,
      '/home/sandbox/notes/r.txt': 'relative',
      '/w/s.txt': 'chunk-1 chunk-2',
      '/w/many/1.txt': 'one',
      '/w/many/3.txt': 'three',
    });
    expect(dirs).toContain('/w/data');
    expect(progress).toEqual([8, 15]);
    expect(results.map((r) => [r.path, r.success])).toEqual([
      ['/w/many/1.txt', true],
      ['/ro/2.txt', false],
      ['/w/many/3.txt', true],
    ]);
    expect(results[1]!.error).toContain('read-only file system');

    expect(FakeWebSocket.instances).toHaveLength(1);
    const query = Object.fromEntries(FakeWebSocket.instances[0]!.url.searchParams);
    expect(query).toMatchObject({
      authorization: 'bearer mo_pat:user:secret',
      organizationId: 'org-1',
      clusterId: 'cluster-1',
      type: 'PORT_FORWARD',
      cmd: 'port-forward',
      namespace: 'agent-sandbox',
      kind: 'Pod',
      workloadName: 'default-k27tp',
      mode: 'ssh',
    });
    expect(query).not.toHaveProperty('remotePort');
    expect(users).toEqual(['sandbox']);
    expect(calls.map((c) => c.url.pathname)).toEqual([
      '/sandbox/agent-sandbox/default-abc12',
      '/resource/exec/agent-sandbox/default-k27tp',
    ]);
    await sb.delete();
  });

  it('reports a file the sandbox refuses as a file error and keeps the session', async () => {
    const { sb } = await sandbox(SshGatewaySocket);

    const error = await sb.fs.uploadFile('x', '/ro/x.txt', { transport: 'sftp' }).catch((err: unknown) => err);
    expect(error).toBeInstanceOf(MogeniusError);
    expect(error).toMatchObject({ source: 'operator', errorCode: undefined });
    expect((error as Error).message).toContain('Upload to /ro/x.txt failed');
    await sb.fs.uploadFile('y', '/w/y.txt', { transport: 'sftp' });

    expect(FakeWebSocket.instances).toHaveLength(1);
    await sb.delete();
  });

  it("takes the client's fileTransport, the call's transport wins", async () => {
    const { sb, calls } = await sandbox(SshGatewaySocket, { fileTransport: 'sftp' });

    await sb.fs.uploadFile('over sftp', '/w/s.txt');
    await sb.fs.uploadFile('over http', '/w/h.txt', { transport: 'http' });

    expect(files.get('/w/s.txt')?.toString()).toBe('over sftp');
    expect(calls.filter((c) => c.url.pathname.endsWith('/upload'))).toHaveLength(1);
    await sb.delete();

    process.env.MOGENIUS_FILE_TRANSPORT = 'SFTP';
    expect(() => new Mogenius(CONFIG)).not.toThrow();
    process.env.MOGENIUS_FILE_TRANSPORT = 'ftp';
    expect(() => new Mogenius(CONFIG)).toThrow(MogeniusValidationError);
  });

  it('closes the session when idle and on delete', async () => {
    sftpTiming.idleMs = 20;
    const { sb } = await sandbox(SshGatewaySocket);

    await sb.fs.uploadFile('x', '/w/x', { transport: 'sftp' });
    await until(() => SshGatewaySocket.closed === 1, 'the idle close');
    await sb.fs.uploadFile('x', '/w/x', { transport: 'sftp' });
    await until(() => SshGatewaySocket.closed === 2, 'the second idle close');
    expect(FakeWebSocket.instances).toHaveLength(2);

    sftpTiming.idleMs = 60_000;
    await sb.fs.uploadFile('x', '/w/x', { transport: 'sftp' });
    await sb.delete();
    expect(SshGatewaySocket.closed).toBe(3);
  });
});

describe('SFTP unavailable', () => {
  /** Starts an upload and plays the gateway with `play` once the stream is open. */
  async function refused(play: (socket: FakeWebSocket) => void) {
    const { sb, calls } = await sandbox(FakeWebSocket);
    const pending = sb.fs.uploadFile('x', '/w/x', { transport: 'sftp' }).catch((err: unknown) => err);
    await until(() => FakeWebSocket.instances.length === 1, 'the gateway stream');
    play(FakeWebSocket.instances[0]!);
    const error = (await pending) as MogeniusError;
    expect(error.errorCode).toBe(ERROR_CODE_SFTP_UNAVAILABLE);
    expect(calls.filter((c) => c.url.pathname.endsWith('/upload'))).toHaveLength(0);
    return error;
  }

  it('says so when the gateway is switched off', async () => {
    const error = await refused((socket) => socket.frame('ERROR: the SSH gateway is disabled'));
    expect(error).toMatchObject({ source: 'operator' });
    expect(error.message).toContain('SSH gateway is disabled');
  });

  it('keeps the kind when the gateway refuses the key', async () => {
    const error = await refused((socket) => socket.frame('UNAUTHORIZED'));
    expect(error).toBeInstanceOf(MogeniusForbiddenError);
  });

  it('says so when no SSH server answers behind the stream', async () => {
    const error = await refused((socket) => {
      socket.frame('PEER_IS_READY');
      const opened = (): string | undefined => socket.sent.find((s) => typeof s === 'string' && s.startsWith('PFM:O:'));
      void until(() => opened() !== undefined, 'the connection').then(() =>
        socket.frame(`PFM:C:${opened()!.slice('PFM:O:'.length)}`),
      );
    });
    expect(error.message).toContain('SSH gateway refused the connection');
  });
});
