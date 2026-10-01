import { describe, expect, it } from 'vitest';
import { Mogenius, MogeniusProcessExecutionTimeoutError } from '../src/index.js';
import { buildCodeRunCommand, extractCharts, shellQuote } from '../src/process.js';
import { CONFIG, fakeFetch, sandboxInfo } from './helpers.js';

const execResponse = (overrides: Record<string, unknown> = {}) => ({
  exitCode: 0,
  result: 'out\n',
  artifacts: { stdout: 'out\n', stderr: '' },
  truncated: false,
  container: 'sandbox',
  durationMs: 40,
  ...overrides,
});

describe('Process.executeCommand', () => {
  it('posts to the toolbox route with Daytona positional parameters', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      {
        status: 201,
        body: execResponse({ exitCode: 3, result: 'out\nerr\n', artifacts: { stdout: 'out\n', stderr: 'err\n' } }),
      },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const response = await sandbox.process.executeCommand('echo out; echo err >&2; exit 3', '/tmp', { FOO: 'bar' }, 30);

    expect(calls[1]!.url.pathname).toBe('/sandbox/agent-sandbox/default-abc12/toolbox/process/execute');
    expect(calls[1]!.body).toEqual({
      command: 'echo out; echo err >&2; exit 3',
      cwd: '/tmp',
      env: { FOO: 'bar' },
      timeout: 30,
    });
    expect(response).toEqual({ exitCode: 3, result: 'out\nerr\n', artifacts: { stdout: 'out\n', stderr: 'err\n' } });
  });

  it('defaults the timeout to 10 seconds and omits empty cwd and env', async () => {
    const { fetch, calls } = fakeFetch({ body: sandboxInfo() }, { status: 201, body: execResponse() });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await sandbox.process.executeCommand('true', undefined, {});
    expect(calls[1]!.body).toEqual({ command: 'true', timeout: 10 });
  });

  it('raises the process timeout error class on 408', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      {
        status: 408,
        body: {
          statusCode: 408,
          errorCode: 'SANDBOX_EXEC_TIMEOUT',
          source: 'operator',
          message: 'exec: command timed out after 2s',
        },
      },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.process.executeCommand('sleep 30', undefined, undefined, 2)).rejects.toBeInstanceOf(
      MogeniusProcessExecutionTimeoutError,
    );
  });

  it('getUserRootDir and getWorkDir ask the sandbox', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: execResponse({ result: '/home/coder', artifacts: { stdout: '/home/coder', stderr: '' } }) },
      {
        status: 201,
        body: execResponse({
          result: '/home/coder/project\n',
          artifacts: { stdout: '/home/coder/project\n', stderr: '' },
        }),
      },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    expect(await sandbox.getUserRootDir()).toBe('/home/coder');
    expect(await sandbox.getWorkDir()).toBe('/home/coder/project');
  });
});

describe('Process.codeRun', () => {
  it('runs Python by default and strips chart artifacts out of stdout', async () => {
    const chart = { type: 'line', title: 'sales', png: 'iVBOR' };
    const stdout = `hello\ndtn_artifact_k39fd2:${JSON.stringify(chart)}\nbye\n`;
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: execResponse({ result: stdout, artifacts: { stdout, stderr: '' } }) },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const response = await sandbox.process.codeRun('print("hello")', { argv: ['a b'], env: { X: '1' } }, 20);

    const command = (calls[1]!.body as { command: string }).command;
    expect(command).toContain('python3 "$__mo_f" \'a b\'');
    expect(command).toContain(Buffer.from('print("hello")').toString('base64'));
    expect(command).toMatch(/exit \$__mo_rc$/);
    expect((calls[1]!.body as { env: unknown }).env).toEqual({ X: '1' });
    expect(response.result).toBe('hello\nbye\n');
    expect(response.artifacts?.charts).toEqual([chart]);
  });

  it('uses tsx for TypeScript and node for JavaScript', async () => {
    const { fetch, calls } = fakeFetch({ body: sandboxInfo() }, { status: 201, body: execResponse() });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12', 'typescript');
    await sandbox.process.codeRun('console.log(1)');
    expect((calls[1]!.body as { command: string }).command).toContain('tsx "$__mo_f"');
    await sandbox.process.codeRun('console.log(1)', { language: 'javascript' });
    expect((calls[2]!.body as { command: string }).command).toContain('node "$__mo_f"');
    expect((calls[2]!.body as { command: string }).command).toContain('.js');
  });
});

describe('helpers', () => {
  it('shellQuote survives single quotes', () => {
    expect(shellQuote(`it's`)).toBe(`'it'\\''s'`);
  });

  it('buildCodeRunCommand keeps user content inside the base64 literal', () => {
    const command = buildCodeRunCommand(`print('x'); import os`, 'python', ['--flag', `va'lue`]);
    expect(command).not.toContain(`print('x')`);
    expect(command).toContain(`'--flag' 'va'\\''lue'`);
  });

  it('extractCharts leaves malformed markers alone', () => {
    const { text, charts } = extractCharts('a\ndtn_artifact_k39fd2:{not json}\nb\n');
    expect(charts).toEqual([]);
    expect(text).toContain('{not json}');
  });
});
