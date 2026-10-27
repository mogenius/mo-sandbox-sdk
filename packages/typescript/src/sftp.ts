import type * as Ssh2 from 'ssh2';
import type { Client as SshClient, SFTPWrapper, Stats } from 'ssh2';
import type { ApiClient } from './api-client.js';
import { MogeniusError, MogeniusNotFoundError } from './errors.js';
import { openStreamTunnel } from './tunnel.js';
import type { Tunnel, UploadProgress } from './types.js';

/**
 * `errorCode` of an upload over SFTP that found no way into the sandbox: the
 * stream gateway or the cluster's SSH gateway refused the connection, cannot
 * do mode=ssh or is switched off. Nothing was written; the caller decides
 * whether to upload over HTTP instead.
 */
export const ERROR_CODE_SFTP_UNAVAILABLE = 'SFTP_UNAVAILABLE';

/** How long a session stays open without an upload, and the SSH handshake's limit; tests shorten them. */
export const sftpTiming = { idleMs: 5000, handshakeMs: 30000 };

/** SFTP writes in flight per upload: the gateway's round trip would otherwise pace every 32 KB. */
const WRITES_IN_FLIGHT = 16;
/** SSH_FX_NO_SUCH_FILE */
const NO_SUCH_FILE = 2;

/**
 * A sandbox's SFTP connection: a port-forward stream in mode=ssh to the
 * operator's SSH gateway, an SSH connection through it and the sftp subsystem
 * on top.
 */
interface Session {
  pod: string;
  tunnel: Pick<Tunnel, 'createConnection' | 'close'>;
  ssh: SshClient;
  sftp: SFTPWrapper;
  workDir?: string;
  users: number;
  idle?: ReturnType<typeof setTimeout>;
  closed: boolean;
}

/**
 * The uploads of one sandbox over SFTP: one session, opened by the first
 * upload and shared by the next ones; it closes after `sftpTiming.idleMs`
 * without an upload, when it breaks, and on the sandbox's `stop()` and
 * `delete()`. An open session keeps Node's event loop alive until then.
 */
export class SftpUploads {
  private pending: Promise<Session> | undefined;
  private current: Session | undefined;

  constructor(
    private readonly api: ApiClient,
    private readonly namespace: string,
    private readonly pod: () => Promise<{ podName: string; containerName: string }>,
    private readonly workDir: () => Promise<string>,
  ) {}

  /**
   * Writes what `source` yields to `path`, creating parent folders as the HTTP
   * upload does, and returns the absolute path. A transfer that fails leaves
   * the remote file as it was: the session is dropped instead of closing the
   * file, and the gateway discards what it staged.
   */
  async upload(
    source: AsyncIterable<Uint8Array> | Iterable<Uint8Array>,
    path: string,
    onProgress?: (progress: UploadProgress) => void,
  ): Promise<string> {
    const session = await this.acquire();
    let broken = false;
    let target = path;
    const fail = (step: string, err: unknown): MogeniusError => {
      if (!isFileError(err)) {
        broken = true;
        return new MogeniusError(
          `Upload to ${target} failed: the SFTP connection broke while ${step}: ${(err as Error).message}`,
          { source: 'operator' },
        );
      }
      return new MogeniusError(`Upload to ${target} failed: ${step}: ${(err as Error).message}`, {
        source: 'operator',
      });
    };
    try {
      target = await this.resolve(session, path);
      try {
        await mkdirAll(session.sftp, parentOf(target));
      } catch (err) {
        throw fail('creating its folder', err);
      }
      let handle: Buffer;
      try {
        handle = await call<Buffer>((done) => session.sftp.open(target, 'w', done));
      } catch (err) {
        throw fail('opening it', err);
      }
      let position = 0;
      let failure: unknown;
      const inFlight = new Set<Promise<void>>();
      try {
        for await (const chunk of source) {
          if (failure !== undefined) {
            break;
          }
          position += chunk.byteLength;
          onProgress?.({ bytesSent: position });
          const offset = position - chunk.byteLength;
          const write: Promise<void> = call<void>((done) =>
            session.sftp.write(
              handle,
              Buffer.from(chunk.buffer, chunk.byteOffset, chunk.byteLength),
              0,
              chunk.byteLength,
              offset,
              done,
            ),
          ).then(
            () => void inFlight.delete(write),
            (err: unknown) => {
              failure ??= err;
              inFlight.delete(write);
            },
          );
          inFlight.add(write);
          if (inFlight.size >= WRITES_IN_FLIGHT) {
            await Promise.race(inFlight);
          }
        }
      } catch (err) {
        // not close(): that would put what arrived so far over the file
        broken = true;
        throw new MogeniusError(`Failed to read the upload's source: ${(err as Error).message}`, { source: 'sdk' });
      }
      await Promise.all(inFlight);
      if (failure !== undefined) {
        broken = true;
        throw fail('writing it', failure);
      }
      try {
        await call<void>((done) => session.sftp.close(handle, done));
      } catch (err) {
        throw fail('writing it', err);
      }
      return target;
    } finally {
      this.release(session, broken);
    }
  }

  /** Closes the session, if one is open; the sandbox's `stop()` and `delete()` call it. */
  close(): void {
    if (this.current) {
      this.drop(this.current);
    }
  }

  private async acquire(): Promise<Session> {
    const { podName, containerName } = await this.pod();
    const pending = this.pending;
    if (pending) {
      // throws when that open failed: the next upload tries again
      const session = await pending;
      if (!session.closed && session.pod === podName) {
        session.users++;
        clearTimeout(session.idle);
        return session;
      }
      // the sandbox was restarted, the old pod is gone
      this.drop(session);
      return this.acquire();
    }
    const opening = this.open(podName, containerName);
    this.pending = opening;
    opening.then(
      (session) => {
        this.current = session;
      },
      () => {
        if (this.pending === opening) {
          this.pending = undefined;
        }
      },
    );
    const session = await opening;
    session.users++;
    return session;
  }

  private release(session: Session, broken: boolean): void {
    session.users--;
    if (broken) {
      this.drop(session);
    } else if (session.users === 0 && !session.closed) {
      session.idle = setTimeout(() => {
        if (session.users === 0) {
          this.drop(session);
        }
      }, sftpTiming.idleMs);
    }
  }

  private drop(session: Session): void {
    if (this.current === session) {
      this.current = undefined;
      this.pending = undefined;
    }
    if (session.closed) {
      return;
    }
    session.closed = true;
    clearTimeout(session.idle);
    session.sftp.end();
    session.ssh.end();
    void session.tunnel.close();
  }

  /**
   * Opens the port-forward in mode=ssh (no remotePort: the gateway picks the
   * pod's SSH gateway), logs in with "none" — the stream is authenticated with
   * the API key, the user name picks the container — and starts the sftp
   * subsystem. The gateway's host key is not pinned: TLS and the key already
   * secure the stream.
   */
  private async open(podName: string, containerName: string): Promise<Session> {
    const ssh2 = (await import('ssh2')) as typeof Ssh2 & { default?: typeof Ssh2 };
    const Client = ssh2.Client ?? ssh2.default!.Client;
    let tunnel: Pick<Tunnel, 'createConnection' | 'close'>;
    try {
      tunnel = await openStreamTunnel(this.api, { namespace: this.namespace, podName, mode: 'ssh' });
    } catch (err) {
      throw unavailable('opening the stream to the SSH gateway', err);
    }
    const ssh = new Client();
    try {
      await new Promise<void>((resolve, reject) => {
        ssh.once('ready', resolve);
        ssh.once('error', reject);
        ssh.once('close', () => reject(new Error('the connection closed during the handshake')));
        ssh.connect({
          sock: tunnel.createConnection(),
          username: containerName,
          hostVerifier: () => true,
          readyTimeout: sftpTiming.handshakeMs,
        });
      });
    } catch (err) {
      ssh.end();
      void tunnel.close();
      throw unavailable('the SSH gateway refused the connection', err);
    }
    let sftp: SFTPWrapper;
    try {
      sftp = await call<SFTPWrapper>((done) => ssh.sftp(done));
    } catch (err) {
      ssh.end();
      void tunnel.close();
      throw unavailable('the SSH gateway refused the sftp subsystem', err);
    }
    const session: Session = { pod: podName, tunnel, ssh, sftp, users: 0, closed: false };
    // a connection that breaks takes its session with it; the next upload opens a new one
    ssh.on('error', () => this.drop(session));
    ssh.on('close', () => this.drop(session));
    return session;
  }

  /** A relative path is relative to the container's working directory, as for the HTTP upload. */
  private async resolve(session: Session, path: string): Promise<string> {
    if (path.startsWith('/')) {
      return normalize(path);
    }
    session.workDir ??= await this.workDir();
    return normalize(`${session.workDir}/${path}`);
  }
}

/**
 * `err` as an SFTP_UNAVAILABLE error of the same kind; a missing pod stays as
 * it is — HTTP would not get further.
 */
function unavailable(what: string, err: unknown): Error {
  if (err instanceof MogeniusNotFoundError) {
    return err;
  }
  if (err instanceof MogeniusError) {
    const Kind = err.constructor as new (
      message: string,
      details: ConstructorParameters<typeof MogeniusError>[1],
    ) => MogeniusError;
    return new Kind(`SFTP: ${what}: ${err.message}`, {
      statusCode: err.statusCode,
      errorCode: ERROR_CODE_SFTP_UNAVAILABLE,
      source: err.source,
    });
  }
  return new MogeniusError(`SFTP: ${what}: ${(err as Error).message}`, {
    errorCode: ERROR_CODE_SFTP_UNAVAILABLE,
    source: 'operator',
  });
}

/** Whether `err` is the server's answer about the file (path, rights, space) rather than a broken connection. */
function isFileError(err: unknown): boolean {
  return typeof (err as { code?: unknown } | undefined)?.code === 'number';
}

async function mkdirAll(sftp: SFTPWrapper, dir: string): Promise<void> {
  if (dir === '/') {
    return;
  }
  try {
    const stats = await call<Stats>((done) => sftp.stat(dir, done));
    if (stats.isDirectory()) {
      return;
    }
  } catch (err) {
    if ((err as { code?: unknown }).code !== NO_SUCH_FILE) {
      throw err;
    }
  }
  await mkdirAll(sftp, parentOf(dir));
  try {
    await call<void>((done) => sftp.mkdir(dir, done));
  } catch (err) {
    // another upload may have created it meanwhile
    const stats = await call<Stats>((done) => sftp.stat(dir, done)).catch(() => undefined);
    if (!stats?.isDirectory()) {
      throw err;
    }
  }
}

/** Runs a node-style call as a promise. */
function call<T>(start: (done: (err: Error | null | undefined, value: T) => void) => void): Promise<T> {
  return new Promise((resolve, reject) => {
    start((err, value) => (err ? reject(err) : resolve(value)));
  });
}

function parentOf(path: string): string {
  const index = path.lastIndexOf('/');
  return index <= 0 ? '/' : path.slice(0, index);
}

/** Resolves `.` and `..` and repeated slashes of an absolute path. */
function normalize(path: string): string {
  const parts: string[] = [];
  for (const part of path.split('/')) {
    if (part === '..') {
      parts.pop();
    } else if (part !== '' && part !== '.') {
      parts.push(part);
    }
  }
  return `/${parts.join('/')}`;
}
