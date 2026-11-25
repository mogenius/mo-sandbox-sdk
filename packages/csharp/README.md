# Mogenius.Sandboxes

C# SDK for mogenius sandboxes — the TypeScript SDK's surface (`@mogenius/sandbox`) in .NET. Targets `net10.0` and `net11.0`.

```csharp
using Mogenius.Sandboxes;

using var mogenius = new MogeniusClient(); // MOGENIUS_API_KEY, MOGENIUS_CLUSTER_ID, … from the environment
var sandbox = await mogenius.CreateAsync(); // claimed from the warm pool, ready in seconds
var result = await sandbox.Process.ExecuteCommandAsync("python3 --version");
Console.WriteLine($"{result.ExitCode} {result.Result}");
await mogenius.DeleteAsync(sandbox);
```

## Mapping from TypeScript

| TypeScript                              | C#                                                         |
| --------------------------------------- | ---------------------------------------------------------- |
| `new Mogenius(config)`                  | `new MogeniusClient(MogeniusOptions)` (`IDisposable`)      |
| `mogenius.create(params, { timeout })`  | `CreateAsync(CreateSandboxParams, TimeSpan? timeout)`      |
| `sandbox.process.*`                     | `sandbox.Process.*Async` (`ProcessService`)                |
| `sandbox.fs.*`                          | `sandbox.Fs.*Async` (`FileSystemService`)                  |
| `executeCommandStream()` async iterator | `ExecuteCommandStreamAsync()` → `IAsyncEnumerable<ExecEvent>` |
| `downloadFileStream()` web stream       | `DownloadFileStreamAsync()` → resuming `Stream`            |
| `uploadFileStream(readable)`            | `UploadFileStreamAsync(Stream)`                            |
| `onProgress` callbacks                  | `IProgress<UploadProgress>` / `IProgress<DownloadProgress>` |
| `sandbox.tunnel(port)` → `Tunnel`       | `TunnelAsync(port)` → `Tunnel` (`IAsyncDisposable`)        |
| `tunnel.createConnection()` → `Duplex`  | `tunnel.CreateConnection()` → `Stream`                     |
| `MogeniusError` and subclasses          | `MogeniusException` and subclasses (`…NotFoundException`, …) |
| `error.source`                          | `exception.ErrorSource`                                    |
| `FileInfo`, `Match`                     | `SandboxFileInfo`, `FileMatch` (no clash with `System.IO` / `Regex`) |
| timeouts in seconds                     | `TimeSpan`; intervals stay in minutes                      |

Every method takes an optional `CancellationToken`. `MogeniusOptions.HttpClient` and
`MogeniusOptions.WebSocketFactory` replace `fetch` and `WebSocket` for tests and custom agents.

SFTP uploads (`FileTransport.Sftp`) use [SSH.NET](https://github.com/sshnet/SSH.NET): the SSH connection runs
through a tunnel's local listener on 127.0.0.1 to the cluster's SSH gateway. The Python bootstrap for `CodeRunAsync`
is the Go SDK's `code_run.py`, embedded; a test keeps them identical.

## Development

```bash
cd packages/csharp
dotnet build
dotnet test     # xUnit v3 on Microsoft.Testing.Platform; fake HttpMessageHandler and WebSocket, no network
```
