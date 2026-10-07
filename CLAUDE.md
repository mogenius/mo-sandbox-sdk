# CLAUDE.md - mo-sandbox-sdk

## Project Overview

`mo-sandbox-sdk` holds the SDKs and API clients for mogenius sandboxes (epic MOG-4647). The public surface
is designed for drop-in use in agent frameworks. Sandboxes are `SandboxClaim` /
`Sandbox` objects of kubernetes-sigs/agent-sandbox; the platform API (`mo-platform-api-service`, module
`mo-sandbox-nest`) and the operator do the work. This repo talks HTTP to the platform only.

## Layout

```
packages/typescript/   @mogenius/sandbox — Mogenius, Sandbox, Process, FileSystem, errors
openapi-specs/         planned: sandbox-api.json, toolbox.json (MOG-4698) → generated clients (MOG-4699)
examples/              runnable with `npx tsx`, read .env
```

npm workspaces; run `npm install` at the root.

## Commands

```bash
npm run build        # tsup, ESM + CJS + d.ts
npm run test         # vitest
npm run lint         # eslint (typescript-eslint, prettier)
npm run format
```

## Rules

- **Established names first.** Methods, parameters and error classes keep the names agent frameworks expect (`snapshot` = profile,
  `target` = namespace, `autoDeleteInterval` = lifetime). mogenius-only additions are marked in the docs.
- **No parallel structures.** Routes, DTO shapes and error codes come from the platform
  (`@mogenius/client-sdk`: lifecycle in `mo-sandbox`, exec and files in `mo-kubernetes/k8s-pod`); mirror them, do not invent new ones here.
- **Unsupported is explicit.** What Kubernetes pods cannot do throws `MogeniusUnsupportedError` with the
  reason and the alternative; never silently degrade.
- **Tests mock `fetch`** (`test/helpers.ts`), never the classes. Cover the request shape and the error
  mapping for every new call.
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
| `process.executeCommand()`, `codeRun()` | `POST /sandbox/:namespace/:id/toolbox/process/execute`  |
| `process.executeCommandStream()`      | stream gateway `/xterm-stream?type=CLUSTER__POD_EXEC&cmd=exec&namespace&podName&container&binary=1`; on the text frame `SEND_EXEC_REQUEST` the client sends one text frame with the `K8sPodExecRequestDto` JSON (never in the URL: Cloudflare blocks shell syntax there, base64 too). Then binary frames tag 0 stdout / 1 stderr; text `EXIT:<code>`, `TRUNCATED`, `TIMEOUT`, `ERROR:<msg>` |
| `fs.listFiles()`, `fs.getFileDetails()` | `GET …/toolbox/files` · `…/files/info`                  |
| `fs.downloadFile()`, `fs.downloadFileStream()` | `POST …/toolbox/files/download-link` → `GET <one-time url>` (fallback `GET …/files/download`) |
| `fs.uploadFile(s)()`                  | `POST …/toolbox/files/upload`                             |
| `fs.createFolder()`, `fs.moveFiles()`, `fs.deleteFile()` | `POST …/files/folder` · `POST …/files/move` · `DELETE …/files` |
| `fs.setFilePermissions()`             | `POST …/toolbox/files/permissions`                        |
| `fs.searchFiles()`, `fs.findFiles()`, `fs.replaceInFiles()` | `GET …/files/search` · `GET …/files/find` · `POST …/files/replace` |
| `process.createSession()`, `listSessions()`, `getSession()`, `deleteSession()` | pod routes, not sandbox routes: `POST`/`GET /resource/session/:namespace/:podName` · `GET`/`DELETE …/:sessionId` (`podName` from the sandbox data; `container` defaults to the sandbox's) |
| `process.executeSessionCommand()`      | `POST /resource/session/:namespace/:podName/:sessionId/exec` (`timeout` = wait of a synchronous call, the command runs on) |
| `process.getSessionCommand()`, `getSessionCommandLogs()`, `sendSessionCommandInput()` | `GET …/:sessionId/command/:cmdId` · `GET …/command/:cmdId/logs` · `POST …/command/:cmdId/input`; live follow over the stream gateway `type=CLUSTER__POD_SESSION_LOG&cmd=session-log&namespace&podName&sessionId&cmdId` with the exec stream's frames |

Headers: `authorization: Bearer <key>`, `organization-id`, `cluster-id`, optional `workspace-name`.
