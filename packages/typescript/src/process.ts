import type { ApiClient } from './api-client.js';
import { MogeniusConflictError, MogeniusError, MogeniusNotFoundError } from './errors.js';
import type { Chart, CodeLanguage, CodeRunParams, ExecEvent, ExecuteResponse } from './types.js';

/** Default when a call names no timeout. */
export const DEFAULT_COMMAND_TIMEOUT_SECONDS = 10;

/**
 * Frames of an exec stream, as the operator writes them: output behind a
 * one-byte stream tag, control messages as text.
 */
const STREAM_TAG_STDERR = 1;
const STREAM_EXIT_PREFIX = 'EXIT:';
const STREAM_ERROR_PREFIX = 'ERROR:';
const STREAM_TRUNCATED = 'TRUNCATED';
const STREAM_TIMEOUT = 'TIMEOUT';
// the gateway asks for the command once the connection is authorized; it does not travel in the URL
const STREAM_REQUEST_PROMPT = 'SEND_EXEC_REQUEST';

/** What the platform answers to the toolbox exec route. */
interface ExecuteCommandResponseBody {
  exitCode: number;
  result: string;
  artifacts: { stdout: string; stderr: string };
  truncated: boolean;
  container: string;
  durationMs: number;
}

/** Marker a Python run prints around each chart (a common convention, kept so existing parsers carry over). */
const CHART_MARKER = /dtn_artifact_k39fd2:(\{.*?\})\n?/g;

/**
 * Processes of a sandbox: run a command or a snippet of code and
 * get exit code and output back. Everything goes through the platform API to
 * the operator, which executes inside the pod without a TTY — no agent in the
 * image, no open port.
 */
export class Process {
  constructor(
    private readonly api: ApiClient,
    private readonly namespace: string,
    private readonly sandboxId: string,
    private readonly defaultLanguage: CodeLanguage,
    /** Where the sandbox runs right now; streams address the pod directly. */
    private readonly pod: () => { podName: string | null; containerName: string },
  ) {}

  /**
   * mogenius: runs a command like `executeCommand`, but delivers its output
   * while it runs — for builds, tests and servers. Yields stdout and stderr
   * chunks as they arrive and ends with one `exit` event carrying the exit
   * code. Leaving the loop early (`break`, `return`) closes the stream, which
   * stops the command in the container within seconds. The command is
   * stateless and gets no stdin, like `executeCommand`.
   *
   * Needs `organizationId`, `clusterId` and the stream gateway (`streamUrl`).
   *
   * @param command the command line
   * @param cwd working directory inside the container; the container's own when absent
   * @param env environment variables for this run; keys must be shell identifiers
   * @param timeout seconds until the command is stopped (default 10); the platform caps it
   */
  async *executeCommandStream(
    command: string,
    cwd?: string,
    env?: Record<string, string>,
    timeout: number = DEFAULT_COMMAND_TIMEOUT_SECONDS,
  ): AsyncGenerator<ExecEvent, void, void> {
    const { podName, containerName } = this.pod();
    if (!podName) {
      throw new MogeniusConflictError(
        'The sandbox has no pod yet: wait until it is started before streaming a command.',
      );
    }
    const socket = this.api.openStream({
      type: 'CLUSTER__POD_EXEC',
      cmd: 'exec',
      namespace: this.namespace,
      podName,
      container: containerName,
    });
    // the same body as the toolbox route, sent once the gateway asks for it
    const request = JSON.stringify({
      command,
      ...(cwd ? { cwd } : {}),
      ...(env && Object.keys(env).length > 0 ? { env } : {}),
      ...(timeout > 0 ? { timeout: Math.ceil(timeout) } : {}),
    });

    const events = new EventQueue<ExecEvent>();
    let exitCode: number | undefined;
    let truncated = false;
    let timedOut = false;
    socket.onmessage = (message: MessageEvent): void => {
      if (typeof message.data !== 'string') {
        const frame = new Uint8Array(message.data as ArrayBuffer);
        if (frame.length > 1) {
          events.push({ type: frame[0] === STREAM_TAG_STDERR ? 'stderr' : 'stdout', data: frame.subarray(1) });
        }
        return;
      }
      const text = message.data;
      if (text === STREAM_REQUEST_PROMPT) {
        socket.send(request);
      } else if (text.startsWith(STREAM_EXIT_PREFIX)) {
        exitCode = Number(text.slice(STREAM_EXIT_PREFIX.length));
      } else if (text === STREAM_TRUNCATED) {
        truncated = true;
      } else if (text === STREAM_TIMEOUT) {
        timedOut = true;
      } else if (text.startsWith(STREAM_ERROR_PREFIX)) {
        events.fail(new MogeniusError(text.slice(STREAM_ERROR_PREFIX.length), { source: 'operator' }));
      }
      // anything else (PEER_IS_READY, pings) is the gateway talking to itself
    };
    socket.onclose = (event: CloseEvent): void => {
      if (exitCode !== undefined) {
        events.push({ type: 'exit', exitCode, truncated, timedOut });
        events.end();
        return;
      }
      events.fail(streamCloseError(event, this.api.streamUrl));
    };
    socket.onerror = (): void => {
      // the close event that follows carries what can be known
    };

    try {
      for await (const event of events) {
        yield event;
      }
    } finally {
      if (socket.readyState === socket.CONNECTING || socket.readyState === socket.OPEN) {
        socket.close(1000);
      }
    }
  }

  /**
   * Runs a shell command line. Pipes, `&&` and quoting behave as in the
   * image's shell. Stateless: `cd` and `export` do not carry over to the next
   * call — pass `cwd` and `env` instead.
   *
   * @param command the command line
   * @param cwd working directory inside the container; the container's own when absent
   * @param env environment variables for this run; keys must be shell identifiers
   * @param timeout seconds until the command is stopped (default 10); the platform caps it
   */
  async executeCommand(
    command: string,
    cwd?: string,
    env?: Record<string, string>,
    timeout: number = DEFAULT_COMMAND_TIMEOUT_SECONDS,
  ): Promise<ExecuteResponse> {
    const body = await this.api.post<ExecuteCommandResponseBody>(
      `/sandbox/${encodeURIComponent(this.namespace)}/${encodeURIComponent(this.sandboxId)}/toolbox/process/execute`,
      {
        command,
        ...(cwd ? { cwd } : {}),
        ...(env && Object.keys(env).length > 0 ? { env } : {}),
        ...(timeout > 0 ? { timeout: Math.ceil(timeout) } : {}),
      },
    );
    return {
      exitCode: body.exitCode,
      result: body.result,
      artifacts: { stdout: body.artifacts?.stdout ?? '', stderr: body.artifacts?.stderr ?? '' },
    };
  }

  /**
   * Runs a snippet of code with the sandbox's default language (or `params.language`):
   * Python through `python3`, TypeScript through `tsx`, JavaScript through `node`.
   * The code travels base64-encoded, so quotes and newlines need no escaping.
   */
  async codeRun(
    code: string,
    params?: CodeRunParams,
    timeout: number = DEFAULT_COMMAND_TIMEOUT_SECONDS,
  ): Promise<ExecuteResponse> {
    const language = params?.language ?? this.defaultLanguage;
    const response = await this.executeCommand(
      buildCodeRunCommand(code, language, params?.argv ?? []),
      undefined,
      params?.env,
      timeout,
    );
    if (language !== 'python') {
      return response;
    }
    const { text, charts } = extractCharts(response.artifacts?.stdout ?? '');
    return {
      exitCode: response.exitCode,
      result: text + (response.artifacts?.stderr ?? ''),
      artifacts: { stdout: text, stderr: response.artifacts?.stderr ?? '', ...(charts.length > 0 ? { charts } : {}) },
    };
  }
}

/** The error a stream that closed without an exit code stands for. */
function streamCloseError(event: CloseEvent, streamUrl: string): MogeniusError {
  const reason = event.reason || '';
  if (reason === 'POD_DOES_NOT_EXIST') {
    return new MogeniusNotFoundError('The sandbox pod does not exist (any more).', { source: 'operator' });
  }
  if (event.code === 1006) {
    return new MogeniusError(
      `Could not connect to the stream gateway at ${streamUrl}: check MOGENIUS_STREAM_URL, the key, and that organizationId and clusterId are set and allow commands in this pod.`,
      { source: 'sdk' },
    );
  }
  if (event.code === 1000) {
    return new MogeniusError('The stream closed before the command reported an exit code.', { source: 'api' });
  }
  return new MogeniusError(reason || `The stream closed with code ${event.code}.`, { source: 'api' });
}

/**
 * Hands socket events over to an async iterator: `push` queues, `end`
 * finishes after what is queued, `fail` rejects the waiting (or next) read.
 */
class EventQueue<T> implements AsyncIterable<T> {
  private readonly queue: T[] = [];
  private done = false;
  private error: Error | undefined;
  private wake: (() => void) | undefined;

  push(item: T): void {
    this.queue.push(item);
    this.wake?.();
  }

  end(): void {
    this.done = true;
    this.wake?.();
  }

  fail(error: Error): void {
    if (this.done) {
      return;
    }
    this.error = error;
    this.done = true;
    this.wake?.();
  }

  async *[Symbol.asyncIterator](): AsyncGenerator<T, void, void> {
    for (;;) {
      if (this.queue.length > 0) {
        yield this.queue.shift()!;
        continue;
      }
      if (this.error) {
        throw this.error;
      }
      if (this.done) {
        return;
      }
      await new Promise<void>((resolve) => {
        this.wake = resolve;
      });
      this.wake = undefined;
    }
  }
}

/** Single-quotes a value for a POSIX shell. */
export function shellQuote(value: string): string {
  return `'${value.replaceAll("'", `'\\''`)}'`;
}

/**
 * The command line that writes the snippet to a temp file, runs it with the
 * language's interpreter and removes the file again, preserving the
 * interpreter's exit code. Everything the user wrote is inside the base64
 * literal; argv entries are quoted one by one.
 */
export function buildCodeRunCommand(code: string, language: CodeLanguage, argv: string[]): string {
  const encoded = Buffer.from(code, 'utf8').toString('base64');
  const args = argv.map(shellQuote).join(' ');
  const { extension, interpreter } = INTERPRETERS[language];
  const file = `/tmp/mo_code_$$.${extension}`;
  return (
    `__mo_f=${file}; printf %s '${encoded}' | base64 -d > "$__mo_f" && ` +
    `${interpreter} "$__mo_f"${args ? ` ${args}` : ''}; __mo_rc=$?; rm -f "$__mo_f"; exit $__mo_rc`
  );
}

const INTERPRETERS: Record<CodeLanguage, { extension: string; interpreter: string }> = {
  python: { extension: 'py', interpreter: 'python3' },
  typescript: { extension: 'ts', interpreter: 'tsx' },
  javascript: { extension: 'js', interpreter: 'node' },
};

/** Splits chart artifacts out of stdout; the rest is the program's own output. */
export function extractCharts(stdout: string): { text: string; charts: Chart[] } {
  const charts: Chart[] = [];
  const text = stdout.replace(CHART_MARKER, (_match, json: string) => {
    try {
      charts.push(JSON.parse(json) as Chart);
    } catch {
      // not a chart after all: keep the line
      return _match;
    }
    return '';
  });
  return { text, charts };
}
