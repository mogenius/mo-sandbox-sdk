# CLAUDE.md - mo-sandbox-sdk

## Project Overview

`mo-sandbox-sdk` holds the SDKs and API clients for mogenius sandboxes (epic MOG-4647). The public surface
is designed for drop-in use in agent frameworks. Sandboxes are `SandboxClaim` /
`Sandbox` objects of kubernetes-sigs/agent-sandbox; the platform API (`mo-platform-api-service`, module
`mo-sandbox-nest`) and the operator do the work. This repo talks HTTP to the platform only.

## Layout

```
packages/typescript/   @mogenius/sandbox — Mogenius, Sandbox, Process, FileSystem, errors
packages/go/           Go module github.com/mogenius/mo-sandbox-sdk/packages/go — pkg/mogenius (Client, Sandbox,
                       ProcessService, FileSystemService), pkg/types, pkg/options, pkg/errors; examples/ inside
openapi-specs/         planned: sandbox-api.json, pod-api.json (MOG-4698) → generated clients (MOG-4699)
examples/              runnable with `npx tsx`, read .env
```

npm workspaces; run `npm install` at the root. The Go module stands alone (tags `packages/go/vX.Y.Z`).

## Commands

```bash
npm run build        # tsup, ESM + CJS + d.ts
npm run test         # vitest
npm run lint         # eslint (typescript-eslint, prettier)
npm run format

cd packages/go && go test ./... && go vet ./... && gofmt -l .
```

## Rules

- **Established names first.** Methods, parameters and error classes keep the names agent frameworks expect (`snapshot` = profile,
  `target` = namespace, `autoDeleteInterval` = lifetime). mogenius-only additions are marked in the docs.
- **No parallel structures.** Routes, DTO shapes and error codes come from the platform
  (`@mogenius/client-sdk`: lifecycle in `mo-sandbox`, exec and files in `mo-kubernetes/k8s-pod`); mirror them, do not invent new ones here.
- **Unsupported is explicit.** What Kubernetes pods cannot do throws `MogeniusUnsupportedError` with the
  reason and the alternative; never silently degrade.
- **Tests mock `fetch`** (`test/helpers.ts`), never the classes. Cover the request shape and the error
  mapping for every new call. Go: the same against an `httptest` platform (`pkg/mogenius/helpers_test.go`).
- **Both SDKs move together.** A new route or call lands in TypeScript and Go; Go mirrors the TypeScript
  behaviour with Go's established shapes (functional options, `context.Context` first, `map[string]any` where
  agent frameworks return maps).
- **codeRun's Python bootstrap exists twice** (`packages/typescript/src/python-bootstrap.ts`,
  `packages/go/pkg/mogenius/code_run.py`): it runs the snippet and prints matplotlib figures as `__mo_chart__:` lines.
  Change both; `TestPythonBootstrapMatchesTypeScript` fails when they differ.
- Prettier: 120 columns, single quotes, trailing commas. ESM with `.js` import suffixes.
- Git: never push or open PRs on your own; see the root CLAUDE.md of mogenius-ai-config.

## Platform routes used

| SDK                                   | Route                                                     |
| ------------------------------------- | --------------------------------------------------------- |
| `create()`                            | `POST /sandbox/:namespace`                                |
| `list()`, `findOne()`                 | `GET /sandbox/:namespace?labels&state&limit&cursor`       |
| `get()`, `refreshData()`              | `GET /sandbox/:namespace/:id`                             |
| `setLabels()`, `setAutoDeleteInterval()` | `PATCH /sandbox/:namespace/:id`                        |
| `start()`, `stop()`                   | `POST /sandbox/:namespace/:id/start` · `/stop`            |
| `delete()`                            | `DELETE /sandbox/:namespace/:id`                          |
| `process.executeCommand()`, `codeRun()` | pod route, not a sandbox route: `POST /resource/exec/:namespace/:podName` (`podName` from the sandbox data, refreshed once when still null; `container` in the body, the sandbox's by default; the stream takes it as query) |
| `process.executeCommandStream()`      | stream gateway `/xterm-stream?type=CLUSTER__POD_EXEC&cmd=exec&namespace&podName&container&binary=1`; on the text frame `SEND_EXEC_REQUEST` the client sends one text frame with the `K8sPodExecRequestDto` JSON (never in the URL: Cloudflare blocks shell syntax there, base64 too). Then binary frames tag 0 stdout / 1 stderr; text `EXIT:<code>`, `TRUNCATED`, `TIMEOUT`, `ERROR:<msg>` |
| `fs.listFiles()`, `fs.getFileDetails()` | pod routes: `GET /resource/files/:namespace/:podName` · `…/info`; every file route takes `?container=` (the sandbox's) |
| `fs.downloadFile()`, `fs.downloadFileStream()` | `POST …/download-link` → `GET <one-time url>` |
| `fs.uploadFile(s)()`                  | `POST …/upload`                                           |
| `fs.createFolder()`, `fs.moveFiles()`, `fs.deleteFile()` | `POST …/folder` · `POST …/move` · `DELETE /resource/files/:namespace/:podName` |
| `fs.setFilePermissions()`             | `POST …/permissions`                                      |
| `fs.searchFiles()`, `fs.findFiles()`, `fs.replaceInFiles()` | `GET …/search` · `GET …/find` · `POST …/replace` |
| `process.createSession()`, `listSessions()`, `getSession()`, `deleteSession()` | pod routes, not sandbox routes: `POST`/`GET /resource/session/:namespace/:podName` · `GET`/`DELETE …/:sessionId` (`podName` from the sandbox data; `container` defaults to the sandbox's) |
| `process.executeSessionCommand()`      | `POST /resource/session/:namespace/:podName/:sessionId/exec` (`timeout` = wait of a synchronous call, the command runs on) |
| `process.getSessionCommand()`, `getSessionCommandLogs()`, `sendSessionCommandInput()` | `GET …/:sessionId/command/:cmdId` · `GET …/command/:cmdId/logs` · `POST …/command/:cmdId/input`; live follow over the stream gateway `type=CLUSTER__POD_SESSION_LOG&cmd=session-log&namespace&podName&sessionId&cmdId` with the exec stream's frames |
| `sandbox.tunnel()` / `Sandbox.Tunnel()` | stream gateway `/xterm-stream?type=PORT_FORWARD&cmd=port-forward&namespace&kind=Pod&workloadName=<pod>&remotePort` (no `binary`; mocli's port-forward protocol): wait for `PEER_IS_READY`, answer it; per TCP connection text `PFM:O:<id>` / `PFM:C:<id>`, bytes as binary `[id length][id][data]`; `BROWSER_PING` every 5 s. Not the HTTP tunnel API (`/resource/tunnel/session`): its proxy forwards the caller's `Authorization`/`Cookie` to the target |

Headers: `authorization: Bearer <key>`, `organization-id`, `cluster-id`, optional `workspace-name`.
