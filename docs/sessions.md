# Sessions

A session is a long-lived shell inside the sandbox. State carries over between commands: `cd`, `export`,
an activated virtualenv. Commands in a session run one at a time.

Use a session when you need state, when a command runs in the background (a dev server, an agent), when it runs
longer than the cluster's limit for a plain command, or when it reads from stdin.

## Lifecycle

| TypeScript                               | Go                                           | Does                                   |
| ---------------------------------------- | -------------------------------------------- | -------------------------------------- |
| `createSession(id, options?)`            | `CreateSession(ctx, id, opts...)`            | opens a shell                          |
| `getSession(id)`                         | `GetSession(ctx, id)`                        | the session and its commands           |
| `listSessions()`                         | `ListSessions(ctx)`                          | sessions your key opened, still alive  |
| `deleteSession(id)`                      | `DeleteSession(ctx, id)`                     | ends the shell and a running command   |
| `executeSessionCommand(id, request)`     | `ExecuteSessionCommand(ctx, id, cmd, async, _)` | runs a command                     |
| `getSessionCommand(id, cmdId)`           | `GetSessionCommand(ctx, id, cmdId)`          | a command and, once ended, its exit code |
| `getSessionCommandLogs(id, cmdId)`       | `GetSessionCommandLogs(ctx, id, cmdId)`      | output so far                          |
| `getSessionCommandLogs(id, cmdId, onLogs)` | `GetSessionCommandLogsStream(ctx, id, cmdId, stdout, stderr)` | follows live until it ends |
| `sendSessionCommandInput(id, cmdId, data)` | `SendSessionCommandInput(ctx, id, cmdId, data)` | writes to the command's stdin     |

A session ends with `deleteSession`, after 30 minutes without a running command (operator setting), when the
sandbox stops, or when a command runs `exit`.

A command keeps the first 16 MiB of its output, stdout and stderr together (operator setting
`MO_SESSION_COMMAND_MAX_OUTPUT_BYTES`); what comes after is dropped while the command runs on, and the command
reports `truncated: true`.

> [!NOTE]
> Sessions are bound to the API key that opened them: another key neither sees nor uses them.

## State carries over

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.process.createSession('dev');

await sandbox.process.executeSessionCommand('dev', { command: 'cd /tmp && export FOO=bar' });
const { output, exitCode } = await sandbox.process.executeSessionCommand('dev', { command: 'echo $FOO $PWD' });
console.log(output, exitCode); // "bar /tmp\n" 0

// a Python virtualenv stays active
await sandbox.process.executeSessionCommand('dev', { command: 'python3 -m venv .venv && . .venv/bin/activate' });
await sandbox.process.executeSessionCommand('dev', { command: 'pip install requests', timeout: 120 });
await sandbox.process.executeSessionCommand('dev', { command: 'python -c "import requests; print(requests.__version__)"' });

await sandbox.process.deleteSession('dev');
```

</details>

<details>
<summary><b>Go</b></summary>

```go
err := sandbox.Process.CreateSession(ctx, "dev")
_, err = sandbox.Process.ExecuteSessionCommand(ctx, "dev", "cd /tmp && export FOO=bar", false, false)
result, err := sandbox.Process.ExecuteSessionCommand(ctx, "dev", "echo $FOO $PWD", false, false)
fmt.Println(result["stdout"], result["exitCode"]) // "bar /tmp\n" 0
err = sandbox.Process.DeleteSession(ctx, "dev")
```

</details>

In Go the result is a `map[string]any` with `id` (the command id), `exitCode` (`int32`, once ended), `stdout`
and `output` (both: stdout and stderr in arrival order — the shell writes them into one stream).

## Synchronous and background commands

A synchronous call waits up to `timeout` seconds (default 60). `runAsync: true` returns right away with the
`cmdId`.

> [!IMPORTANT]
> **The command is not stopped when the wait runs out** — the answer then has no `exitCode`; fetch the result
> later with `getSessionCommand`.

<details open>
<summary><b>TypeScript</b></summary>

```ts
// background: a dev server keeps running after the call returns
const server = await sandbox.process.executeSessionCommand('web', {
  command: 'python3 -m http.server 8000',
  runAsync: true,
});
console.log(server.cmdId);

// a wait that runs out
const slow = await sandbox.process.executeSessionCommand('dev', { command: 'sleep 90; echo done', timeout: 5 });
console.log(slow.exitCode); // undefined: still running
// … later
const cmd = await sandbox.process.getSessionCommand('dev', slow.cmdId);
console.log(cmd.command, cmd.exitCode);
```

</details>

<details>
<summary><b>Go</b></summary>

```go
started, err := sandbox.Process.ExecuteSessionCommand(ctx, "web", "python3 -m http.server 8000", true, false)
cmdID := started["id"].(string)

cmd, err := sandbox.Process.GetSessionCommand(ctx, "web", cmdID)
fmt.Println(cmd["command"], cmd["exitCode"]) // exitCode absent while running
```

</details>

## Logs

<details open>
<summary><b>TypeScript</b></summary>

```ts
// what it printed so far
const text = await sandbox.process.getSessionCommandLogs('dev', cmdId);

// follow live: first what was printed already, then every new chunk; resolves when the command ends
await sandbox.process.getSessionCommandLogs('dev', cmdId, (chunk) => process.stdout.write(chunk));
const { exitCode } = await sandbox.process.getSessionCommand('dev', cmdId);
```

</details>

<details>
<summary><b>Go</b></summary>

```go
logs, err := sandbox.Process.GetSessionCommandLogs(ctx, "dev", cmdID)
fmt.Print(logs.Output)

// live: both channels are closed when the command ends
stdout, stderr := make(chan string), make(chan string)
go func() {
	for chunk := range stdout {
		fmt.Print(chunk)
	}
}()
go func() {
	for chunk := range stderr {
		fmt.Fprint(os.Stderr, chunk)
	}
}()
err = sandbox.Process.GetSessionCommandLogsStream(ctx, "dev", cmdID, stdout, stderr)
```

</details>

Following live goes through the stream gateway and needs `organizationId` and `clusterId` in the config.

## Input

Write to the standard input of the running command — what `read` or an interactive program waits for. Data is
written verbatim, so end lines with `\n`.

<details open>
<summary><b>TypeScript</b></summary>

```ts
const ask = await sandbox.process.executeSessionCommand('dev', {
  command: 'read name; echo hello $name',
  runAsync: true,
});
await sandbox.process.sendSessionCommandInput('dev', ask.cmdId, 'jane\n');
await sandbox.process.getSessionCommandLogs('dev', ask.cmdId, (c) => process.stdout.write(c)); // hello jane
```

</details>

<details>
<summary><b>Go</b></summary>

```go
err = sandbox.Process.SendSessionCommandInput(ctx, "dev", cmdID, "jane\n")
```

</details>

## Environment and secrets

*mogenius addition.* `env` at creation is exported into the session's shell, so every command sees it.

> [!CAUTION]
> A command's text is listed with the session (`getSession`), its environment is not — **put API keys into
> `env`, never into the command line**.

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.process.createSession('agent', {
  env: { ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY!, LINEAR_API_KEY: process.env.LINEAR_API_KEY! },
});

// in another container of the pod
await sandbox.process.createSession('sidecar', { container: 'worker' });
await sandbox.process.createSession('sidecar2', 'worker'); // short form: just the container
```

</details>

<details>
<summary><b>Go</b></summary>

```go
err := sandbox.Process.CreateSession(ctx, "agent",
	options.WithSessionEnv(map[string]string{"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY")}),
	options.WithSessionContainer("worker"),
)
```

</details>

## Long-running agents

A session command has no time limit of its own, unlike `executeCommand` (5 minutes by default on the cluster).
Run an agent as a background session command and follow its logs:

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.process.createSession('agent', { env: { ANTHROPIC_API_KEY: key } });
const { cmdId } = await sandbox.process.executeSessionCommand('agent', {
  command: 'claude -p "Fix the failing tests in /home/coder/project" --permission-mode bypassPermissions',
  runAsync: true,
});
await sandbox.process.getSessionCommandLogs('agent', cmdId, (chunk) => process.stdout.write(chunk));
const { exitCode } = await sandbox.process.getSessionCommand('agent', cmdId);
```

</details>

[`packages/go/examples/chat`](../packages/go/examples/chat) holds a whole multi-turn conversation with Claude Code
this way: one process, messages over stdin, each turn as long as it takes.

## Errors you may see

| Error                      | When                                                           |
| -------------------------- | -------------------------------------------------------------- |
| `MogeniusConflictError`    | session id taken (`SESSION_ALREADY_EXISTS`), a command still running (`SESSION_BUSY`) |
| `MogeniusNotFoundError`    | no such session (`SESSION_NOT_FOUND`) or command (`SESSION_COMMAND_NOT_FOUND`) |
| `MogeniusOperatorUpgradeRequiredError` | the cluster's operator is too old for sessions     |

---

<p align="center"><a href="commands.md">← Commands and code</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="files.md">Files →</a></p>
