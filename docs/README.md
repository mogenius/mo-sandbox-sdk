<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <img alt="mogenius" src="assets/logo-light.svg" width="280">
  </picture>
</p>

<h1 align="center">Sandbox SDK</h1>

<p align="center">
  Isolated, short-lived environments for AI agents and code execution —<br>
  running as pods in <b>your own Kubernetes cluster</b>.
</p>

<p align="center">
  <a href="../packages/typescript"><img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-%40mogenius%2Fsandbox-3178C6?logo=typescript&logoColor=white"></a>
  <a href="../packages/go"><img alt="Go" src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white"></a>
  <img alt="Node" src="https://img.shields.io/badge/Node-22%2B-5FA04E?logo=nodedotjs&logoColor=white">
  <img alt="Kubernetes" src="https://img.shields.io/badge/runs%20on-Kubernetes-326CE5?logo=kubernetes&logoColor=white">
  <a href="../LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

<p align="center">
  <a href="getting-started.md"><b>Getting started</b></a> ·
  <a href="recipes.md"><b>Recipes</b></a> ·
  <a href="api-reference.md"><b>API reference</b></a>
</p>

---

Create a sandbox in seconds, run commands and code in it, keep long-lived shells, move files, and reach its
ports — from TypeScript or Go.

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create();
const { exitCode, result } = await sandbox.process.executeCommand('python3 --version');
await mogenius.delete(sandbox);
```

## Contents

| Guide                                    | What you learn                                                          |
| ---------------------------------------- | ----------------------------------------------------------------------- |
| [Getting started](getting-started.md)    | Install, create an API key, configure the client, run a first sandbox    |
| [Concepts](concepts.md)                  | Profiles, warm pools, states, how a call reaches the pod                 |
| [Sandboxes](sandboxes.md)                | Create, warm pools, find, list, start, stop, delete, labels, lifetime, auto-stop |
| [Commands and code](commands.md)         | `executeCommand`, `codeRun` with charts, live output streams            |
| [Sessions](sessions.md)                  | Stateful shells, background commands, live logs, stdin                  |
| [Files](files.md)                        | Upload, download, list, move, permissions, search, replace              |
| [Tunnels](tunnels.md)                    | Reach an HTTP server, WebSocket, gRPC or database inside the sandbox    |
| [Errors](errors.md)                      | The error hierarchy and how to branch on it                             |
| [Recipes](recipes.md)                    | End-to-end examples: agents, test runs, web previews, data analysis     |
| [API reference](api-reference.md)        | Every function of both SDKs on one page                                 |

## SDKs

| Language   | Package                                                      | Docs                                              |
| ---------- | ------------------------------------------------------------ | ------------------------------------------------- |
| TypeScript | `@mogenius/sandbox` (GitHub Packages)                        | [packages/typescript](../packages/typescript)     |
| Go         | `github.com/mogenius/mo-sandbox-sdk/packages/go`             | [packages/go](../packages/go)                     |

Both SDKs offer the same features. The examples in these guides show TypeScript first, then Go.

## Runnable examples

| Example                                                  | Shows                                                   |
| -------------------------------------------------------- | ------------------------------------------------------- |
| [`examples/basic.ts`](../examples/basic.ts)              | create, a command, Python and TypeScript code, delete   |
| [`examples/stream.ts`](../examples/stream.ts)            | live output, exit codes, timeouts, stopping early       |
| [`examples/sessions.ts`](../examples/sessions.ts)        | sessions, background commands, live logs, stdin         |
| [`examples/fs.ts`](../examples/fs.ts)                    | every file operation, including permission checks       |
| [`packages/go/examples/basic`](../packages/go/examples/basic)   | the basics in Go                                 |
| [`packages/go/examples/stream`](../packages/go/examples/stream) | live output in Go                                |
| [`packages/go/examples/tunnel`](../packages/go/examples/tunnel) | a web server in the sandbox, reached via tunnel  |
| [`packages/go/examples/chat`](../packages/go/examples/chat)     | a long conversation with Claude Code in a sandbox |

---

<p align="center"><a href="getting-started.md">Getting started →</a></p>
