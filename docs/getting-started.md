# Getting started

## Prerequisites

- A mogenius organization with a cluster connected, and the **Sandboxes** feature installed on that cluster
  (the sandbox chart with at least one profile, `default` by default).
- An API key: **Organization → API keys** in the mogenius platform. Keys look like `mo_pat:…`. The key needs
  the *editor* role on the cluster (or the workspace) for anything that changes a sandbox; *viewer* keys can
  only read.
- TypeScript: Node.js 22 or newer (global `fetch` and `WebSocket`). Go: Go 1.25 or newer.

## Install

The package is published to GitHub Packages. Point the `@mogenius` scope there in your `.npmrc`, with a GitHub
token that has `read:packages`:

```ini
@mogenius:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_NPM_TOKEN}
```

```bash
npm install @mogenius/sandbox
```

<details>
<summary><b>Go</b></summary>

```bash
go get github.com/mogenius/mo-sandbox-sdk/packages/go@latest
```

Releases are tagged `packages/go/vX.Y.Z`. If `GOPRIVATE` covers `github.com/mogenius`, Go fetches with git
directly and needs access to GitHub.

</details>

## Configure

Both SDKs read their configuration from the environment. Explicit values in code win; empty values fall back
to the environment.

| Environment variable         | TypeScript option | Go field         | Default                              |
| ---------------------------- | ----------------- | ---------------- | ------------------------------------ |
| `MOGENIUS_API_KEY`           | `apiKey`          | `APIKey`         | **required**                         |
| `MOGENIUS_API_URL`           | `apiUrl`          | `APIUrl`         | `https://platform-api.mogenius.com`  |
| `MOGENIUS_ORGANIZATION_ID`   | `organizationId`  | `OrganizationID` | taken from the key if it has one     |
| `MOGENIUS_CLUSTER_ID`        | `clusterId`       | `ClusterID`      | taken from the key if it has one     |
| `MOGENIUS_SANDBOX_NAMESPACE` | `namespace` (alias `target`) | `Namespace` (alias `Target`) | `agent-sandbox` |
| `MOGENIUS_SANDBOX_PROFILE`   | `profile`         | `Profile`        | `default`                            |
| `MOGENIUS_WORKSPACE_NAME`    | `workspaceName`   | `WorkspaceName`  | none — for keys without a cluster role |
| `MOGENIUS_STREAM_URL`        | `streamUrl`       | `StreamURL`      | `wss://k8s-cmd-stream.mogenius.com`  |
| —                            | `fetch`, `webSocket` | `HTTPClient`  | the global ones / `http.DefaultClient` |

> [!IMPORTANT]
> **Streams and tunnels need both ids.** `executeCommandStream`, live session logs and `tunnel` go through the
> stream gateway, which does not read organization and cluster from the key. Set `MOGENIUS_ORGANIZATION_ID` and
> `MOGENIUS_CLUSTER_ID` whenever you use them.

A typical `.env`:

```bash
MOGENIUS_API_KEY=mo_pat:...
MOGENIUS_ORGANIZATION_ID=00000000-0000-0000-0000-000000000000
MOGENIUS_CLUSTER_ID=00000000-0000-0000-0000-000000000000
```

Load it into the shell before running a program:

```bash
set -a; . ./.env; set +a
```

> [!WARNING]
> `node --env-file=.env` does not override a `MOGENIUS_*` variable your shell already exports. If a run uses the
> wrong key or cluster, check `env | grep MOGENIUS`.

## Your first sandbox

```ts
import { Mogenius } from '@mogenius/sandbox';

// reads MOGENIUS_* from the environment; or: new Mogenius({ apiKey: '…', clusterId: '…' })
const mogenius = new Mogenius();

// claimed from the warm pool of the configured profile, started in seconds
const sandbox = await mogenius.create({ labels: { project: 'hello' } });
console.log(`sandbox ${sandbox.id} is ${sandbox.state}`);

try {
  const shell = await sandbox.process.executeCommand('echo hello from $(hostname)');
  console.log(shell.exitCode, shell.result);

  const python = await sandbox.process.codeRun('print(sum(range(10)))');
  console.log(python.result); // 45
} finally {
  await mogenius.delete(sandbox);
}
```

<details>
<summary><b>Go</b></summary>

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/mogenius"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func main() {
	ctx := context.Background()

	client, err := mogenius.NewClient() // reads MOGENIUS_* from the environment
	if err != nil {
		log.Fatal(err)
	}

	sandbox, err := client.Create(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"project": "hello"}},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer sandbox.Delete(ctx)

	shell, err := sandbox.Process.ExecuteCommand(ctx, "echo hello from $(hostname)")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(shell.ExitCode, shell.Result)

	python, err := sandbox.Process.CodeRun(ctx, "print(sum(range(10)))")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(python.Result) // 45
}
```

Explicit configuration in Go:

```go
client, err := mogenius.NewClientWithConfig(&types.MogeniusConfig{
	APIKey:    os.Getenv("MY_KEY"),
	ClusterID: "00000000-0000-0000-0000-000000000000",
	Profile:   "python-data",
})
```

</details>

## Next

- [Concepts](concepts.md) — what a profile and a warm pool are
- [Sandboxes](sandboxes.md) — the full lifecycle
- [Recipes](recipes.md) — complete programs to copy from

---

<p align="center"><a href="README.md">← Overview</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="concepts.md">Concepts →</a></p>
