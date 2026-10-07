import type { Server, Socket } from 'node:net';
import type { ApiClient } from './api-client.js';
import { MogeniusError, MogeniusForbiddenError, MogeniusNotFoundError, MogeniusTimeoutError } from './errors.js';
import { streamCloseError } from './process.js';
import type { Tunnel, TunnelOptions } from './types.js';

/**
 * Frames of a port-forward stream. Each TCP connection through the tunnel has
 * an id: text frames open and close it, binary frames carry its bytes behind
 * a one-byte id length and the id.
 */
const OPEN_PREFIX = 'PFM:O:';
const CLOSE_PREFIX = 'PFM:C:';
const READY = 'PEER_IS_READY';
const READY_ACK = 'ack-ready';
const PING = 'BROWSER_PING';
const PEER_CLOSED = 'CLOSE_CONNECTION_FROM_PEER';

const PING_INTERVAL_MS = 5000;
const READY_TIMEOUT_MS = 30000;
/** Above this much unsent data a local connection pauses until the socket drains. */
const HIGH_WATER_BYTES = 4 << 20;

/** Pauses before each attempt to reconnect, the last one repeating; tests shorten them. */
export const tunnelTiming = { reconnectDelaysMs: [1000, 2000, 4000, 8000, 16000, 30000], drainCheckMs: 50 };

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** Where a tunnel leads: a port of a pod. */
export interface TunnelTarget {
  namespace: string;
  podName: string;
  port: number;
}

/**
 * Opens a tunnel: waits until the operator holds the pod's port, then listens
 * on 127.0.0.1. Node only — the local side is a TCP server.
 */
export async function openTunnel(api: ApiClient, target: TunnelTarget, options: TunnelOptions): Promise<Tunnel> {
  const tunnel = new PortForwardTunnel(api, target);
  await tunnel.start(options.localPort ?? 0);
  return tunnel;
}

/**
 * Forwards TCP connections from a local port to a port of the sandbox through
 * the platform's stream gateway, the way a port-forward does. When the gateway
 * connection drops, the tunnel reconnects on its own; connections open at
 * that moment break.
 */
class PortForwardTunnel implements Tunnel {
  readonly host = '127.0.0.1';
  port = 0;
  readonly done: Promise<Error | undefined>;
  private finish!: (error: Error | undefined) => void;
  private server: Server | undefined;
  /** The live gateway stream; undefined while reconnecting. */
  private socket: WebSocket | undefined;
  /** A stream still waiting for the operator, so `close()` can abort it. */
  private connecting: WebSocket | undefined;
  private readonly connections = new Map<string, Socket>();
  private sequence = 0;
  private ended = false;
  private pingTimer: ReturnType<typeof setInterval> | undefined;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private readonly api: ApiClient,
    private readonly target: TunnelTarget,
  ) {
    this.done = new Promise((resolve) => {
      this.finish = resolve;
    });
  }

  get url(): string {
    return `http://${this.host}:${this.port}`;
  }

  async start(localPort: number): Promise<void> {
    const net = await import('node:net');
    const socket = await this.connect();
    this.socket = socket;
    this.serve(socket);
    const server = net.createServer((local) => this.bridge(local));
    try {
      await new Promise<void>((resolve, reject) => {
        server.once('error', reject);
        server.listen(localPort, this.host, () => {
          server.off('error', reject);
          resolve();
        });
      });
    } catch (err) {
      this.end(undefined);
      throw new MogeniusError(`Could not listen on ${this.host}:${localPort}: ${(err as Error).message}`, {
        source: 'sdk',
      });
    }
    this.server = server;
    this.port = (server.address() as { port: number }).port;
    this.pingTimer = setInterval(() => this.send(PING), PING_INTERVAL_MS);
    this.pingTimer.unref?.();
  }

  async close(): Promise<void> {
    this.end(undefined);
    await this.done;
  }

  /** Opens the gateway stream and waits until the operator holds the port. */
  private connect(): Promise<WebSocket> {
    return new Promise((resolve, reject) => {
      let socket: WebSocket;
      try {
        socket = this.api.openStream({
          type: 'PORT_FORWARD',
          cmd: 'port-forward',
          namespace: this.target.namespace,
          kind: 'Pod',
          workloadName: this.target.podName,
          remotePort: this.target.port,
        });
      } catch (err) {
        reject(err instanceof Error ? err : new MogeniusError(String(err), { source: 'sdk' }));
        return;
      }
      this.connecting = socket;
      const settle = (error?: Error): void => {
        clearTimeout(timer);
        this.connecting = undefined;
        if (!error) {
          resolve(socket);
          return;
        }
        socket.onmessage = null;
        socket.onclose = null;
        if (socket.readyState === socket.CONNECTING || socket.readyState === socket.OPEN) {
          socket.close(1000);
        }
        reject(error);
      };
      const timer = setTimeout(
        () =>
          settle(
            new MogeniusTimeoutError(
              `The cluster operator did not open the tunnel within ${READY_TIMEOUT_MS / 1000} s.`,
              { source: 'sdk' },
            ),
          ),
        READY_TIMEOUT_MS,
      );
      socket.onmessage = (message: MessageEvent): void => {
        if (typeof message.data !== 'string') {
          return;
        }
        const text = message.data;
        if (text === READY || text === READY_ACK) {
          socket.send(READY);
          settle();
        } else if (text.includes('DOES_NOT_EXIST')) {
          settle(new MogeniusNotFoundError(text, { source: 'operator' }));
        } else if (text.includes('UNAUTHORIZED')) {
          settle(new MogeniusForbiddenError(text, { source: 'api' }));
        } else if (text.includes('ERROR')) {
          settle(new MogeniusError(text, { source: 'operator' }));
        }
        // anything else (pings) is the gateway talking to itself
      };
      socket.onclose = (event: CloseEvent): void => {
        settle(
          event.code === 1000 && !event.reason
            ? new MogeniusError('The stream gateway closed the tunnel before the operator opened it.', {
                source: 'api',
              })
            : streamCloseError(event, this.api.streamUrl),
        );
      };
      socket.onerror = (): void => {
        // the close event that follows carries what can be known
      };
    });
  }

  /** Hands each frame of the gateway stream to its connection; a broken stream is reconnected. */
  private serve(socket: WebSocket): void {
    socket.onmessage = (message: MessageEvent): void => {
      if (typeof message.data !== 'string') {
        const frame = new Uint8Array(message.data as ArrayBuffer);
        const head = 1 + (frame[0] ?? 0);
        if (frame.length >= 2 && frame.length >= head) {
          this.connections.get(decoder.decode(frame.subarray(1, head)))?.write(frame.subarray(head));
        }
        return;
      }
      const text = message.data;
      if (text.startsWith(CLOSE_PREFIX)) {
        this.closeConnection(text.slice(CLOSE_PREFIX.length), false);
      } else if (text === PEER_CLOSED) {
        this.end(new MogeniusError('The stream gateway closed the tunnel.', { source: 'api' }));
      }
    };
    socket.onclose = (): void => {
      if (this.ended) {
        return;
      }
      // the connections of the broken stream are gone with it
      this.socket = undefined;
      this.dropConnections();
      this.reconnect(0);
    };
    socket.onerror = (): void => {
      // the close event that follows starts the reconnect
    };
  }

  private reconnect(attempt: number): void {
    const delays = tunnelTiming.reconnectDelaysMs;
    this.reconnectTimer = setTimeout(
      () => {
        this.connect().then(
          (socket) => {
            if (this.ended) {
              socket.close(1000);
              return;
            }
            this.socket = socket;
            this.serve(socket);
          },
          (error: Error) => {
            if (this.ended) {
              return;
            }
            // the pod is gone; under its name it does not come back
            if (error instanceof MogeniusNotFoundError) {
              this.end(error);
              return;
            }
            this.reconnect(attempt + 1);
          },
        );
      },
      delays[Math.min(attempt, delays.length - 1)],
    );
  }

  /** Announces a local connection to the gateway and pumps its bytes there. */
  private bridge(local: Socket): void {
    const socket = this.socket;
    if (!socket || socket.readyState !== socket.OPEN) {
      local.destroy();
      return;
    }
    const id = `c${++this.sequence}`;
    const idBytes = encoder.encode(id);
    this.connections.set(id, local);
    local.setNoDelay(true);
    socket.send(OPEN_PREFIX + id);
    local.on('data', (chunk: Buffer) => {
      const frame = new Uint8Array(1 + idBytes.length + chunk.length);
      frame[0] = idBytes.length;
      frame.set(idBytes, 1);
      frame.set(chunk, 1 + idBytes.length);
      socket.send(frame);
      // the socket buffers what the network has not taken yet: hold the local side until it drains
      if (socket.bufferedAmount > HIGH_WATER_BYTES) {
        local.pause();
        const drain = setInterval(() => {
          if (socket.readyState !== socket.OPEN || socket.bufferedAmount <= HIGH_WATER_BYTES / 4) {
            clearInterval(drain);
            local.resume();
          }
        }, tunnelTiming.drainCheckMs);
      }
    });
    local.on('close', () => this.closeConnection(id, true));
    local.on('error', () => local.destroy());
  }

  /** Ends one connection; `tell` is whether the gateway still has to hear of it. */
  private closeConnection(id: string, tell: boolean): void {
    const local = this.connections.get(id);
    if (!local) {
      return;
    }
    this.connections.delete(id);
    if (tell) {
      this.send(CLOSE_PREFIX + id);
    } else {
      // the pod closed it: what it sent before still reaches the local side
      local.end();
    }
  }

  private dropConnections(): void {
    for (const local of this.connections.values()) {
      local.destroy();
    }
    this.connections.clear();
  }

  private send(text: string): void {
    if (this.socket && this.socket.readyState === this.socket.OPEN) {
      this.socket.send(text);
    }
  }

  private end(error: Error | undefined): void {
    if (this.ended) {
      return;
    }
    this.ended = true;
    clearInterval(this.pingTimer);
    clearTimeout(this.reconnectTimer);
    this.dropConnections();
    for (const socket of [this.socket, this.connecting]) {
      if (socket && (socket.readyState === socket.CONNECTING || socket.readyState === socket.OPEN)) {
        socket.close(1000);
      }
    }
    this.socket = undefined;
    if (this.server) {
      this.server.close(() => this.finish(error));
    } else {
      this.finish(error);
    }
  }
}
