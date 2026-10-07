# mogenius sandbox SDK for Go

Go SDK for mogenius sandboxes: lifecycle, commands, code runs, sessions and files inside sandbox pods on your
own cluster. Module `github.com/mogenius/mo-sandbox-sdk/packages/go`.

```bash
go get github.com/mogenius/mo-sandbox-sdk/packages/go@latest
```

The repository is private, so Go fetches it with your git access. Once per machine:

```bash
go env -w GOPRIVATE='github.com/mogenius/*'
git config --global url."git@github.com:".insteadOf "https://github.com/"   # or a token in ~/.netrc
```

Releases are tagged `packages/go/vX.Y.Z`; `@main` gets the latest commit.

```go
client, err := mogenius.NewClient() // MOGENIUS_API_KEY, MOGENIUS_CLUSTER_ID, … from the environment
sandbox, err := client.Create(ctx, nil) // claimed from the default warm pool, started
result, err := sandbox.Process.ExecuteCommand(ctx, "python3 --version")
fmt.Println(result.ExitCode, result.Result)
err = sandbox.Delete(ctx)
```

## Packages

| Package        | Holds                                                                         |
| -------------- | ----------------------------------------------------------------------------- |
| `pkg/mogenius` | `Client`, `Sandbox`, `ProcessService`, `FileSystemService`, `SandboxState`    |
| `pkg/types`    | `MogeniusConfig`, `SnapshotParams`, `ImageParams`, `ExecuteResponse`, …       |
| `pkg/options`  | functional options: `WithTimeout`, `WithCwd`, `WithCodeRunLanguage`, …        |
| `pkg/errors`   | `MogeniusError` and its kinds; shadows the standard `errors`, import it as `sdkerrors` |

## Configuration

`mogenius.NewClient()` reads the environment; `mogenius.NewClientWithConfig(&types.MogeniusConfig{…})` takes
explicit values, and empty fields still fall back to the environment.

| Field            | Environment variable         | Default                             |
| ---------------- | ---------------------------- | ----------------------------------- |
| `APIKey`         | `MOGENIUS_API_KEY`           | required — an API key (`mo_pat:…`)  |
| `APIUrl`         | `MOGENIUS_API_URL`           | `https://platform-api.mogenius.com` |
| `OrganizationID` | `MOGENIUS_ORGANIZATION_ID`   | taken from the key when it has one  |
| `ClusterID`      | `MOGENIUS_CLUSTER_ID`        | taken from the key when it has one  |
| `Namespace`      | `MOGENIUS_SANDBOX_NAMESPACE` | `agent-sandbox` (alias: `Target`)   |
| `WorkspaceName`  | `MOGENIUS_WORKSPACE_NAME`    | none — for keys without a cluster role |
| `StreamURL`      | `MOGENIUS_STREAM_URL`        | `wss://k8s-cmd-stream.mogenius.com` |
| `HTTPClient`     | —                            | `http.DefaultClient`                |

Streams (`ExecuteCommandStream`, `GetSessionCommandLogsStream`) need `OrganizationID` and `ClusterID` even for
keys with a single scope: the stream gateway does not take them from the key.

## Sandboxes

```go
// from a profile's warm pool: bound and started in seconds
sandbox, err := client.Create(ctx, types.SnapshotParams{
	SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"project": "demo"}},
	Snapshot:          "default",
})

// from your own image, stamped from the profile's template: a pod start
sandbox, err = client.Create(ctx, types.ImageParams{Image: "python:3.12-slim"}, options.WithTimeout(2*time.Minute))
```

`Create` waits until the sandbox is started (`options.WithWaitForStart(false)` returns right away).
`AutoDeleteInterval` is the lifetime in minutes from now; pod and storage go with the sandbox.

| Call                                                    | Does                                               |
| ------------------------------------------------------- | -------------------------------------------------- |
| `client.Get(ctx, id)`                                    | one sandbox by claim or sandbox name               |
| `client.List(ctx, query)` / `client.ListSeq(ctx, query)` | all sandboxes, paged, by labels and states        |
| `sandbox.Start` / `Stop` / `Delete` (`…WithTimeout`)    | lifecycle; stopping keeps the volume               |
| `sandbox.WaitForStart` / `WaitForStop`                  | poll until the state is reached                    |
| `sandbox.SetLabels`, `sandbox.SetAutoDeleteInterval`    | labels; lifetime in minutes from now, nil removes it |
| `sandbox.RefreshData`                                    | reload the fields                                  |
| `sandbox.GetUserHomeDir`, `sandbox.GetWorkingDir`       | asked from the running sandbox                     |

mogenius fields on `Sandbox`: `Namespace`, `Image`, `PodName`, `ContainerName`, `ExpiresAt`, `Ephemeral`, `Kind`.

## Commands and code

```go
result, err := sandbox.Process.ExecuteCommand(ctx, "make test",
	options.WithCwd("/workspace"), options.WithCommandEnv(map[string]string{"CI": "1"}),
	options.WithExecuteTimeout(2*time.Minute))
// a non-zero exit code is a result, not an error
fmt.Println(result.ExitCode, result.Artifacts.Stdout, result.Artifacts.Stderr)

run, err := sandbox.Process.CodeRun(ctx, `print("hello")`) // python3; tsx and node with WithCodeRunLanguage
```

Commands are stateless (`cd` and `export` do not carry over) and default to a 10 s timeout. Figures a Python run
draws with matplotlib come back as PNG charts (type, title, image) in `Artifacts.Charts` — on `plt.show()` and for
what is still open at the end; the image needs matplotlib, nothing else.

mogenius: `ExecuteCommandStream` delivers the output while the command runs. Leaving the loop early stops the
command in the container.

```go
for event, err := range sandbox.Process.ExecuteCommandStream(ctx, "npm test", options.WithExecuteTimeout(5*time.Minute)) {
	if err != nil {
		return err
	}
	switch event.Type {
	case types.ExecEventStdout, types.ExecEventStderr:
		os.Stdout.Write(event.Data)
	case types.ExecEventExit:
		fmt.Println("exit", event.ExitCode, "timed out:", event.TimedOut)
	}
}
```

## Sessions

A session is a long-lived shell where `cd`, `export` and a virtualenv carry over between commands.

```go
err := sandbox.Process.CreateSession(ctx, "build")
result, err := sandbox.Process.ExecuteSessionCommand(ctx, "build", "cd /workspace && npm ci", false, false)
// result["id"], result["exitCode"] (int32), result["stdout"]: stdout and stderr in arrival order
async, err := sandbox.Process.ExecuteSessionCommand(ctx, "build", "npm run dev", true, false)
logs, err := sandbox.Process.GetSessionCommandLogs(ctx, "build", async["id"].(string))
err = sandbox.Process.DeleteSession(ctx, "build")
```

`GetSessionCommandLogsStream` follows a command live into two channels and closes them when it ends;
`SendSessionCommandInput` (mogenius) writes to the command's stdin.

## Files

```go
err := sandbox.FileSystem.UploadFile(ctx, []byte("print(1)"), "/workspace/main.py") // or a local path as string
data, err := sandbox.FileSystem.DownloadFile(ctx, "/workspace/out.csv", nil)          // or &localPath
files, err := sandbox.FileSystem.ListFiles(ctx, "/workspace")
```

`ListFiles`, `GetFileInfo`, `CreateFolder`, `DeleteFile`, `MoveFiles`, `SetFilePermissions`, `SearchFiles`,
`FindFiles` and `ReplaceInFiles` work on paths inside the container. `DownloadFileStream` and
`UploadFileStream` move big files without holding them in memory; a broken download resumes on its own.

## Errors

Every failed call returns one of the kinds in `pkg/errors`, and each unwraps to `*MogeniusError` with
`StatusCode`, `ErrorCode` (the platform's code, e.g. `SANDBOX_NOT_FOUND`) and `Source` (`api`, `operator`, `sdk`).
A cancelled context comes back as itself.

```go
var notFound *sdkerrors.MogeniusNotFoundError
if errors.As(err, &notFound) {
	…
}
```

| Kind                                   | When                                                     |
| -------------------------------------- | -------------------------------------------------------- |
| `MogeniusAuthenticationError`          | the key was rejected (401)                               |
| `MogeniusForbiddenError`               | no grant, no cluster role, RBAC or file permissions (403) |
| `MogeniusNotFoundError`                | no sandbox, container, profile, file, session or command (404) |
| `MogeniusConflictError`                | wrong state: not started, name taken, command running (409) |
| `MogeniusValidationError`              | a bad parameter (400)                                    |
| `MogeniusTimeoutError`                 | a wait ran out (504, or the SDK's own deadline)          |
| `MogeniusProcessExecutionTimeoutError` | the command ran past its timeout (408); also a timeout   |
| `MogeniusOperatorUpgradeRequiredError` | the cluster's operator is too old for the call (424)     |
| `MogeniusUnsupportedError`             | not available on Kubernetes sandboxes, with the alternative |

## Not supported

Sandboxes are Kubernetes pods, not micro-VMs. `Archive`, `Pause`, `ExperimentalFork`,
`ExperimentalCreateSnapshot` and `SetAutoArchiveInterval` return `MogeniusUnsupportedError` naming the
alternative; `AutoStopInterval` and `AutoArchiveInterval` are accepted and not used. Volumes, snapshot
management, PTY, git, code interpreter contexts and computer use are not part of the SDK.

## Development

```bash
cd packages/go
go test ./...
gofmt -l .
```

The examples run against a real platform with the root `.env`:

```bash
set -a; . ../../.env; set +a
go run ./examples/basic
go run ./examples/stream
```
