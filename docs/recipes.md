# Recipes

Complete programs for common jobs. Each one creates a sandbox, does its work and cleans up. Run the TypeScript
ones with `npx tsx recipe.ts` after loading your `.env` (see [Getting started](getting-started.md)).

- [Run untrusted code from an LLM](#run-untrusted-code-from-an-llm)
- [Analyse data and get the charts](#analyse-data-and-get-the-charts)
- [Clone a repo and run its tests](#clone-a-repo-and-run-its-tests)
- [Run a coding agent in the background](#run-a-coding-agent-in-the-background)
- [Preview a web app](#preview-a-web-app)
- [Many sandboxes in parallel](#many-sandboxes-in-parallel)
- [Pause work and continue later](#pause-work-and-continue-later)
- [An LLM tool for code execution](#an-llm-tool-for-code-execution)

## Run untrusted code from an LLM

Code the model wrote runs in the sandbox, never on your machine. A lifetime makes sure nothing stays behind.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius, MogeniusProcessExecutionTimeoutError } from '@mogenius/sandbox';

const mogenius = new Mogenius();

export async function runGenerated(code: string): Promise<string> {
  const sandbox = await mogenius.create({ autoDeleteInterval: 10, labels: { purpose: 'llm-code' } });
  try {
    const run = await sandbox.process.codeRun(code, undefined, 30);
    return run.exitCode === 0 ? run.result : `Error (exit ${run.exitCode}):\n${run.artifacts?.stderr}`;
  } catch (err) {
    if (err instanceof MogeniusProcessExecutionTimeoutError) return 'Error: the code ran longer than 30 seconds.';
    throw err;
  } finally {
    await mogenius.delete(sandbox);
  }
}

console.log(await runGenerated('print([n * n for n in range(5)])')); // [0, 1, 4, 9, 16]
```

</details>

## Analyse data and get the charts

Upload a CSV, let pandas and matplotlib work on it, save the charts locally.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { readFile, writeFile } from 'node:fs/promises';
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ snapshot: 'python-data' }); // a profile with pandas + matplotlib

try {
  await sandbox.fs.uploadFile(await readFile('./sales.csv'), '/tmp/sales.csv');

  const run = await sandbox.process.codeRun(
    `
import pandas as pd
import matplotlib.pyplot as plt

df = pd.read_csv("/tmp/sales.csv", parse_dates=["date"])
monthly = df.groupby(df["date"].dt.to_period("M"))["revenue"].sum()
print(monthly.describe())

monthly.plot(kind="bar", title="Revenue per month")
plt.show()

df.groupby("region")["revenue"].sum().plot(kind="pie", title="By region")
`,
    undefined,
    60,
  );

  console.log(run.result);
  for (const [i, chart] of (run.artifacts?.charts ?? []).entries()) {
    await writeFile(`chart-${i}-${chart.type}.png`, Buffer.from(chart.png!, 'base64'));
    console.log('saved', chart.title);
  }
} finally {
  await mogenius.delete(sandbox);
}
```

</details>

## Clone a repo and run its tests

A session keeps the working directory; the stream shows the test output live.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ autoDeleteInterval: 30 });
const decoder = new TextDecoder();

try {
  await sandbox.process.createSession('ci', { env: { GITHUB_TOKEN: process.env.GITHUB_TOKEN! } });
  const clone = await sandbox.process.executeSessionCommand('ci', {
    command: 'git clone --depth 1 https://$GITHUB_TOKEN@github.com/acme/app.git /tmp/app && cd /tmp/app && npm ci',
    timeout: 300,
  });
  if (clone.exitCode !== 0) throw new Error(clone.output);

  let exitCode = -1;
  for await (const event of sandbox.process.executeCommandStream('npm test', '/tmp/app', { CI: '1' }, 300)) {
    if (event.type === 'exit') exitCode = event.exitCode;
    else process.stdout.write(decoder.decode(event.data));
  }
  console.log(exitCode === 0 ? '✅ tests passed' : `❌ tests failed (${exitCode})`);
} finally {
  await mogenius.delete(sandbox);
}
```

</details>

## Run a coding agent in the background

An agent runs longer than a plain command may. As a background session command it has no time limit; its API
key travels in the session's env, not in the command text.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius({ profile: 'claude-code' }); // a profile whose image has `claude`
const sandbox = await mogenius.create({ labels: { agent: 'claude', task: 'fix-tests' } });

await sandbox.process.createSession('agent', {
  env: { ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY! },
});
const { cmdId } = await sandbox.process.executeSessionCommand('agent', {
  command:
    'cd /home/coder/project && claude -p "Make all tests in this repo pass." --permission-mode bypassPermissions',
  runAsync: true,
});
console.log(`agent running in ${sandbox.id}, command ${cmdId}`);

// follow it live; resolves when the agent ends
await sandbox.process.getSessionCommandLogs('agent', cmdId, (chunk) => process.stdout.write(chunk));
const { exitCode } = await sandbox.process.getSessionCommand('agent', cmdId);
console.log('agent finished with', exitCode);

// fetch the result
const diff = await sandbox.process.executeCommand('git diff', '/home/coder/project', undefined, 30);
console.log(diff.result);
await mogenius.delete(sandbox);
```

</details>

> [!TIP]
> Your program does not have to stay connected: store `sandbox.id` and `cmdId`, and pick it up later with
> `mogenius.get(id)` and `getSessionCommand` / `getSessionCommandLogs`.

The Go example
[`examples/chat`](../packages/go/examples/chat) runs a multi-turn conversation with one `claude` process.

## Preview a web app

Start a dev server inside the sandbox and open it locally through a tunnel.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ image: 'node:22', autoDeleteInterval: 60 }, { timeout: 120 });

await sandbox.fs.uploadFiles([
  {
    destination: '/app/server.js',
    source: `require('http').createServer((q, s) => s.end('<h1>Hello from the sandbox</h1>')).listen(3000);`,
  },
]);
await sandbox.process.createSession('web');
await sandbox.process.executeSessionCommand('web', { command: 'node /app/server.js', runAsync: true });

const tunnel = await sandbox.tunnel(3000, { localPort: 3000 });
console.log(`open ${tunnel.url} — Ctrl+C to stop`);

process.on('SIGINT', async () => {
  await tunnel.close();
  await mogenius.delete(sandbox);
  process.exit(0);
});
```

</details>

The same in Go: [`packages/go/examples/tunnel`](../packages/go/examples/tunnel).

## Many sandboxes in parallel

Warm-pool sandboxes start in seconds; run a matrix side by side.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const versions = ['3.10', '3.11', '3.12', '3.13'];

const results = await Promise.all(
  versions.map(async (version) => {
    const sandbox = await mogenius.create(
      { image: `python:${version}-slim`, labels: { matrix: 'py' }, autoDeleteInterval: 15 },
      { timeout: 180 },
    );
    try {
      const run = await sandbox.process.codeRun('import sys; print(sys.version)');
      return { version, ok: run.exitCode === 0, output: run.result.trim() };
    } finally {
      await mogenius.delete(sandbox);
    }
  }),
);
console.table(results);
```

</details>

<details>
<summary><b>Go</b> (with an errgroup)</summary>

```go
g, ctx := errgroup.WithContext(ctx)
for _, version := range []string{"3.11", "3.12", "3.13"} {
	g.Go(func() error {
		sandbox, err := client.Create(ctx, types.ImageParams{Image: "python:" + version + "-slim"},
			options.WithTimeout(3*time.Minute))
		if err != nil {
			return err
		}
		defer sandbox.Delete(context.Background())
		run, err := sandbox.Process.CodeRun(ctx, "import sys; print(sys.version)")
		if err != nil {
			return err
		}
		fmt.Println(version, run.Result)
		return nil
	})
}
return g.Wait()
```

</details>

## Pause work and continue later

`stop()` frees the pod but keeps the volume. Find the sandbox again by label, start it, and the files are there.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();

// day 1
const sandbox = await mogenius.create({ labels: { user: 'jane', project: 'thesis' } });
await sandbox.fs.uploadFile('draft v1', 'notes.md');
await sandbox.stop();

// day 2 — another process
const again = await mogenius.findOne({ labels: { user: 'jane', project: 'thesis' } });
if (again.state === 'stopped') await again.start();
console.log((await again.fs.downloadFile('notes.md')).toString()); // draft v1
```

</details>

## An LLM tool for code execution

Expose the sandbox as a tool to a model. This uses the Anthropic SDK; any framework with tool calls works the
same way.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import Anthropic from '@anthropic-ai/sdk';
import { Mogenius } from '@mogenius/sandbox';

const anthropic = new Anthropic();
const mogenius = new Mogenius();
const sandbox = await mogenius.create({ autoDeleteInterval: 30 });

const tools: Anthropic.Tool[] = [
  {
    name: 'run_python',
    description: 'Run Python code in an isolated sandbox and return stdout and stderr.',
    input_schema: { type: 'object', properties: { code: { type: 'string' } }, required: ['code'] },
  },
];

const messages: Anthropic.MessageParam[] = [
  { role: 'user', content: 'What is the 30th Fibonacci number? Compute it.' },
];

try {
  for (;;) {
    const response = await anthropic.messages.create({
      model: 'claude-opus-5-5',
      max_tokens: 2048,
      tools,
      messages,
    });
    messages.push({ role: 'assistant', content: response.content });
    if (response.stop_reason !== 'tool_use') {
      console.log(response.content.find((b) => b.type === 'text')?.text);
      break;
    }
    const results: Anthropic.ToolResultBlockParam[] = [];
    for (const block of response.content) {
      if (block.type !== 'tool_use') continue;
      const run = await sandbox.process.codeRun((block.input as { code: string }).code, undefined, 60);
      results.push({ type: 'tool_result', tool_use_id: block.id, content: run.result, is_error: run.exitCode !== 0 });
    }
    messages.push({ role: 'user', content: results });
  }
} finally {
  await mogenius.delete(sandbox);
}
```

</details>

---

<p align="center"><a href="errors.md">← Errors</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="api-reference.md">API reference →</a></p>
