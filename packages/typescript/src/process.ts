import type { ApiClient } from './api-client.js';
import type { Chart, CodeLanguage, CodeRunParams, ExecuteResponse } from './types.js';

/** Default when a call names no timeout. */
export const DEFAULT_COMMAND_TIMEOUT_SECONDS = 10;

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
  ) {}

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
