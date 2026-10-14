# API reference

Every public function of both SDKs. *m* marks mogenius additions. For generated TypeScript docs run
`npm run docs -w @mogenius/sandbox` (TypeDoc, output in `packages/typescript/docs/`); for Go, `go doc` works on
every package.

## Client

| TypeScript                                          | Go                                                    | Returns / does                                       |
| --------------------------------------------------- | ----------------------------------------------------- | ---------------------------------------------------- |
| `new Mogenius(config?)`                             | `mogenius.NewClient()` · `NewClientWithConfig(&cfg)`   | client; config falls back to `MOGENIUS_*`            |
| `create(params?, { timeout? })`                     | `Create(ctx, params, opts...)`                        | `Sandbox`, started unless told not to wait           |
| `get(id, language?)`                                | `Get(ctx, id)`                                        | `Sandbox`                                            |
| `findOne({ id?, labels?, state? })`                 | —                                                     | first match, or `MogeniusNotFoundError`              |
| `list(labels?, { limit?, cursor? }, state?)`        | `List(ctx, query)` · `ListSeq(ctx, query)`            | page `{ items, nextCursor, total }` / iterator       |
| `warmPools({ allNamespaces? })` *m*                 | `WarmPools(ctx, opts...)`                             | `WarmPool[]` of the namespace (or all), by namespace and name; none without agent-sandbox |
| `start(sandbox, timeout?)`                          | —                                                     | `sandbox.start()`                                    |
| `stop(sandbox, timeout?)`                           | —                                                     | `sandbox.stop()`                                     |
| `delete(sandbox)`                                   | —                                                     | `sandbox.delete()`                                   |
| —                                                   | `Close(ctx)`                                          | no-op, for `defer`                                   |
| `snapshot`, `volume`                                | —                                                     | throw `MogeniusUnsupportedError`                     |

Go iterator: `it.Next()`, `it.Value()`, `it.Err()`. Query: `ListSandboxesQuery{ Limit, Labels, States }`.

Go create options (`pkg/options`): `WithTimeout(d)`, `WithWaitForStart(bool)`, `WithLogChannel(ch)`. Warm pool
options: `WithAllNamespaces()`.

## Sandbox

| TypeScript                         | Go                                             | Returns / does                                          |
| ---------------------------------- | ---------------------------------------------- | ------------------------------------------------------- |
| `refreshData()`                    | `RefreshData(ctx)`                             | reloads the fields                                      |
| `start(timeout = 60)`              | `Start(ctx)` · `StartWithTimeout(ctx, d)`      | resumes a stopped sandbox and waits                     |
| `stop(timeout = 60)`               | `Stop(ctx)` · `StopWithTimeout(ctx, d, force)` | pod goes, volume stays; waits                           |
| `delete()`                         | `Delete(ctx)` · `DeleteWithTimeout(ctx, d)`    | removes it                                              |
| `waitUntilStarted(timeout = 60)`   | `WaitForStart(ctx, d)`                         | polls; fails on `error`                                 |
| `waitUntilStopped(timeout = 60)`   | `WaitForStop(ctx, d)`                          | polls                                                   |
| `setLabels(labels)`                | `SetLabels(ctx, labels)`                       | replaces your labels                                    |
| `setAutoDeleteInterval(min)` · `setTtl(min)` | `SetAutoDeleteInterval(ctx, *min)`   | lifetime from now; 0 / nil removes it                   |
| `getUserRootDir()`                 | `GetUserHomeDir(ctx)`                          | home of the container user                              |
| `getWorkDir()`                     | `GetWorkingDir(ctx)`                           | the container's working directory                       |
| `tunnel(port, { localPort? })` *m* | `Tunnel(ctx, port, opts...)` *m*               | [`Tunnel`](tunnels.md)                                  |
| `process`                          | `Process`                                      | [commands](commands.md) and [sessions](sessions.md)     |
| `fs`                               | `FileSystem`                                   | [files](files.md)                                       |
| `fork()`, `archive()`, `setAutostopInterval()`, `setAutoArchiveInterval()` | `ExperimentalFork`, `Archive`, `Pause`, `ExperimentalCreateSnapshot`, `SetAutoArchiveInterval` | `MogeniusUnsupportedError` |

Fields: see [Sandboxes → Fields](sandboxes.md#fields).

## Process — commands and code

| TypeScript                                                       | Go                                                    | Returns                                  |
| ---------------------------------------------------------------- | ----------------------------------------------------- | ---------------------------------------- |
| `executeCommand(cmd, cwd?, env?, timeout = 10, container?)`      | `ExecuteCommand(ctx, cmd, opts...)`                   | `{ exitCode, result, artifacts }`        |
| `codeRun(code, { language?, argv?, env?, container? }?, timeout = 10)` | `CodeRun(ctx, code, opts...)`                   | as above, plus `artifacts.charts`        |
| `executeCommandStream(cmd, cwd?, env?, timeout = 10, container?)` *m* | `ExecuteCommandStream(ctx, cmd, opts...)` *m*    | async iterable / `iter.Seq2` of `ExecEvent` |

Go options: `WithCwd`, `WithCommandEnv`, `WithExecuteTimeout`, `WithContainer` (commands and streams);
`WithCodeRunLanguage`, `WithCodeRunParams`, `WithCodeRunTimeout`, `WithCodeRunContainer` (code).

`ExecEvent`: `{ type: 'stdout' | 'stderr', data }` or `{ type: 'exit', exitCode, truncated, timedOut }`.

## Process — sessions

| TypeScript                                                    | Go                                                                | Returns                                   |
| ------------------------------------------------------------- | ----------------------------------------------------------------- | ----------------------------------------- |
| `createSession(id, container \| { container?, env? }?)`       | `CreateSession(ctx, id, opts...)`                                 | `SessionInfo` / error                     |
| `getSession(id)`                                              | `GetSession(ctx, id)`                                             | `{ sessionId, commands, container, createdAt, lastUsedAt }` |
| `listSessions()`                                              | `ListSessions(ctx)`                                               | sessions of your key                      |
| `deleteSession(id)`                                           | `DeleteSession(ctx, id)`                                          | ends shell and running command            |
| `executeSessionCommand(id, { command, runAsync?, timeout? })` | `ExecuteSessionCommand(ctx, id, cmd, runAsync, suppressInputEcho)` | `{ cmdId, exitCode?, output? }` / map with `id`, `exitCode`, `stdout`, `output` |
| `getSessionCommand(id, cmdId)`                                | `GetSessionCommand(ctx, id, cmdId)`                               | `{ id, command, exitCode?, truncated? }`  |
| `getSessionCommandLogs(id, cmdId)`                            | `GetSessionCommandLogs(ctx, id, cmdId)`                           | output so far                             |
| `getSessionCommandLogs(id, cmdId, onLogs)`                    | `GetSessionCommandLogsStream(ctx, id, cmdId, stdout, stderr)`     | follows live until the command ends       |
| `sendSessionCommandInput(id, cmdId, data)`                    | `SendSessionCommandInput(ctx, id, cmdId, data)`                   | writes to stdin                           |

Go options: `WithSessionContainer`, `WithSessionEnv` *m*.

## FileSystem

| TypeScript                                        | Go                                                  | Returns                               |
| ------------------------------------------------- | --------------------------------------------------- | ------------------------------------- |
| `listFiles(path?)`                                | `ListFiles(ctx, path)`                              | `FileInfo[]`                          |
| `getFileDetails(path)`                            | `GetFileInfo(ctx, path)`                            | `FileInfo`                            |
| `uploadFile(data, path)`                          | `UploadFile(ctx, []byte \| localPath, path)`        | —                                     |
| `uploadFiles([{ source, destination }])`          | —                                                   | `UploadResult[]`                      |
| —                                                 | `UploadFileStream(ctx, reader, path, WithUploadProgress(fn))` | —                           |
| `downloadFile(path)`                              | `DownloadFile(ctx, path, *localPath)`               | `Buffer` / `[]byte`                   |
| `downloadFiles(paths)`                            | —                                                   | `{ path, data?, error? }[]`           |
| `downloadFileStream(path)` *m*                    | `DownloadFileStream(ctx, path, WithDownloadProgress(fn))` | `ReadableStream` / `io.ReadCloser`, resumes |
| `createFolder(path, mode?)`                       | `CreateFolder(ctx, path, WithMode(m))`              | —                                     |
| `moveFiles(source, destination)`                  | `MoveFiles(ctx, source, destination)`               | —                                     |
| `deleteFile(path, recursive = false)`             | `DeleteFile(ctx, path, recursive)`                  | —                                     |
| `setFilePermissions(path, { mode?, owner?, group? })` | `SetFilePermissions(ctx, path, WithPermissionMode, WithOwner, WithGroup)` | `FileInfo` / — |
| `searchFiles(path, glob)`                         | `SearchFiles(ctx, path, glob)`                      | `{ files, truncated }`                |
| `findFiles(path, pattern)`                        | `FindFiles(ctx, path, pattern)`                     | `{ file, line, content }[]`           |
| `replaceInFiles(files, pattern, newValue)`        | `ReplaceInFiles(ctx, files, pattern, newValue)`     | `{ file, success, error }[]`          |

## Tunnel *m*

| TypeScript      | Go                              | Meaning                                         |
| --------------- | ------------------------------- | ----------------------------------------------- |
| `host`, `port`  | `Addr()`                        | local address (`127.0.0.1:<port>`)              |
| `url`           | `URL()`                         | `http://127.0.0.1:<port>`                       |
| `done`          | `Done()`, `Err()`               | when and why it ended                           |
| `close()`       | `Close()`                       | ends it and every connection                    |
| —               | `DialContext(ctx, net, addr)`   | a connection without the local listener         |

Go option: `options.WithLocalPort(port)`.

## Configuration and constants (TypeScript)

| Export                              | Value                                  |
| ----------------------------------- | -------------------------------------- |
| `resolveConfig(config)`             | the config as the SDK resolves it      |
| `DEFAULT_API_URL`                   | `https://platform-api.mogenius.com`    |
| `DEFAULT_STREAM_URL`                | `wss://k8s-cmd-stream.mogenius.com`    |
| `DEFAULT_NAMESPACE`                 | `agent-sandbox`                        |
| `DEFAULT_PROFILE`                   | `default`                              |
| `DEFAULT_COMMAND_TIMEOUT_SECONDS`   | `10`                                   |
| `errorFromResponse(status, body, fallback)` | maps a platform answer to an error class |

## Errors

See [Errors](errors.md).

---

<p align="center"><a href="recipes.md">← Recipes</a> &nbsp;·&nbsp; <a href="README.md">Overview</a></p>
