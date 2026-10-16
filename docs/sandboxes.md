# Sandboxes

Create, find, change and remove sandboxes. See [Concepts](concepts.md) for profiles, warm pools and states.

## Create

`create()` creates a sandbox and, by default, waits until it is `started`.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();

// 1. from the warm pool of the configured profile — ready in seconds
const sandbox = await mogenius.create();

// 2. from the warm pool of a named profile, with labels and a lifetime
const tagged = await mogenius.create({
  snapshot: 'python-data',
  name: 'report-42', // optional; generated when absent
  labels: { project: 'reports', user: 'jane' },
  autoDeleteInterval: 60, // minutes until it is shut down
  language: 'python', // default language of codeRun
});

// 3. from your own image, in the profile's pod template — a regular pod start
const custom = await mogenius.create(
  { image: 'python:3.13-slim', envVars: { MODE: 'test' } },
  { timeout: 120 }, // seconds to wait for "started" (default 60)
);

// 4. do not wait: returns right after creation
const later = await mogenius.create({}, { timeout: 0 });
await later.waitUntilStarted(90);
```

</details>

| Param                | Type                       | Meaning                                                                  |
| -------------------- | -------------------------- | ------------------------------------------------------------------------ |
| `snapshot`           | `string`                   | Profile to use. Default: configured `profile`, otherwise `default`       |
| `image`              | `string`                   | Run this image in the profile's template instead of claiming from the pool |
| `name`               | `string`                   | Name of the sandbox; generated when absent                               |
| `labels`             | `Record<string, string>`   | Kubernetes labels; `list()` and `findOne()` filter by them               |
| `envVars`            | `Record<string, string>`   | Env of the container. On a warm-pool sandbox the profile must allow it    |
| `autoDeleteInterval` | `number` (minutes)         | Lifetime; absent means no deadline                                       |
| `autoStopInterval`   | `number` (minutes)         | Stop after this long without a toolbox call; absent means never ([auto-stop](#auto-stop)) |
| `ephemeral`          | `boolean`                  | Delete pod **and** storage with the sandbox (default `true`)             |
| `language`           | `'python' \| 'typescript' \| 'javascript'` | Default language of `codeRun` (default `python`)         |

<details>
<summary><b>Go</b></summary>

```go
// from the configured profile's warm pool
sandbox, err := client.Create(ctx, nil)

// from a named profile, with labels and a lifetime
lifetime := 60
sandbox, err = client.Create(ctx, types.SnapshotParams{
	SandboxBaseParams: types.SandboxBaseParams{
		Name:               "report-42",
		Labels:             map[string]string{"project": "reports"},
		AutoDeleteInterval: &lifetime,
		Language:           types.CodeLanguagePython,
	},
	Snapshot: "python-data",
})

// from your own image
sandbox, err = client.Create(ctx, types.ImageParams{
	SandboxBaseParams: types.SandboxBaseParams{EnvVars: map[string]string{"MODE": "test"}},
	Image:             "python:3.13-slim",
}, options.WithTimeout(2*time.Minute))

// do not wait
sandbox, err = client.Create(ctx, nil, options.WithWaitForStart(false))
err = sandbox.WaitForStart(ctx, 90*time.Second)
```

`Create` takes `nil`, `types.SnapshotParams` or `types.ImageParams` (value or pointer).
Options: `options.WithTimeout`, `options.WithWaitForStart`, `options.WithLogChannel` (accepted for
compatibility; mogenius builds no images at create time, the channel is closed when `Create` returns).

</details>

## Warm pools

`warmPools()` lists the warm pools — the pre-started sandboxes `create()` claims from — so a UI or a script can
offer the pools that exist instead of guessing a profile name. Without options it lists the client's namespace;
pools may live in any namespace, so `allNamespaces` lists the whole cluster. A cluster without agent-sandbox
has no pools: the list is empty, not an error.

<details open>
<summary><b>TypeScript</b></summary>

```ts
const pools = await mogenius.warmPools(); // the client's namespace
const everywhere = await mogenius.warmPools({ allNamespaces: true }); // every namespace of the cluster

for (const pool of everywhere) {
  console.log(`${pool.namespace}/${pool.name}: ${pool.ready}/${pool.size} ready (template ${pool.template})`);
}

// claim from one of them: the client's namespace has to be the pool's
const client = new Mogenius({ namespace: everywhere[0].namespace });
const sandbox = await client.create({ snapshot: everywhere[0].name });
```

</details>

| Field       | Type     | Meaning                                                          |
| ----------- | -------- | ---------------------------------------------------------------- |
| `name`      | `string` | Name of the pool; `snapshot` in `create()`                       |
| `namespace` | `string` | Namespace of the pool; the client's `namespace` to claim from it |
| `template`  | `string` | SandboxTemplate the pool's pods are stamped from                 |
| `ready`     | `number` | Pre-started sandboxes ready to be claimed; `0` when not reported |
| `size`      | `number` | Sandboxes the pool keeps; `0` when not set                       |

The list is sorted by namespace, then name. Pools are created and sized on the cluster's **Sandboxes** page in
the mogenius platform; the SDK only reads them.

<details>
<summary><b>Go</b></summary>

```go
pools, err := client.WarmPools(ctx)                                    // the client's namespace
everywhere, err := client.WarmPools(ctx, options.WithAllNamespaces()) // every namespace of the cluster

for _, pool := range everywhere {
	fmt.Printf("%s/%s: %d/%d ready (template %s)\n", pool.Namespace, pool.Name, pool.Ready, pool.Size, pool.Template)
}
```

`types.WarmPool` has the fields `Name`, `Namespace`, `Template`, `Ready` and `Size`.

</details>

## Get, find and list

<details open>
<summary><b>TypeScript</b></summary>

```ts
// by id — e.g. stored from an earlier run
const sandbox = await mogenius.get('report-42');

// first match by labels (and state); throws MogeniusNotFoundError when nothing matches
const mine = await mogenius.findOne({ labels: { user: 'jane' }, state: 'started' });
const byId = await mogenius.findOne({ id: 'report-42' });

// one page at a time
let page = await mogenius.list({ project: 'reports' }, { limit: 20 });
console.log(`${page.total} sandboxes`);
for (const s of page.items) console.log(s.id, s.state);
while (page.nextCursor) {
  page = await mogenius.list({ project: 'reports' }, { limit: 20, cursor: page.nextCursor });
  // …
}

// filter by state: third argument
const stopped = await mogenius.list(undefined, {}, 'stopped');
```

</details>

<details>
<summary><b>Go</b></summary>

```go
sandbox, err := client.Get(ctx, "report-42")

// iterator: fetches pages as needed
it := client.List(ctx, &mogenius.ListSandboxesQuery{
	Labels: map[string]string{"project": "reports"},
	States: []mogenius.SandboxState{mogenius.SandboxStateStarted, mogenius.SandboxStateStopped},
})
for it.Next() {
	fmt.Println(it.Value().ID, it.Value().State)
}
if err := it.Err(); err != nil {
	return err
}

// or as a range-over-func sequence
for sandbox, err := range client.ListSeq(ctx, nil) {
	if err != nil {
		return err
	}
	fmt.Println(sandbox.ID)
}
```

</details>

## Fields

| TypeScript         | Go               | Meaning                                                           |
| ------------------ | ---------------- | ----------------------------------------------------------------- |
| `id`               | `ID`             | Name of the claim or sandbox object; what every call takes         |
| `name`             | `Name`           | Display name                                                      |
| `state`            | `State`          | See [states](concepts.md#states)                                  |
| `errorReason`      | `ErrorReason`    | Why the state is `error` or still `starting`                      |
| `labels`           | `Labels`         | Kubernetes labels                                                 |
| `snapshot`         | `Snapshot`       | Profile it was made from                                          |
| `target`, `namespace` | `Target`, `Namespace` | Kubernetes namespace                                    |
| `image`            | `Image`          | Image it runs                                                     |
| `createdAt`        | `CreatedAt`      | ISO timestamp                                                     |
| `expiresAt`        | `ExpiresAt`      | When it shuts down; null without a deadline                       |
| `podName`          | `PodName`        | The pod; null while creating                                      |
| `containerName`    | `ContainerName`  | The container commands and files use by default                   |
| `serviceFQDN`      | `ServiceFQDN`    | Address inside the cluster                                        |
| `sandboxName`      | `SandboxName`    | The `Sandbox` object behind a claim                               |
| `operatingMode`    | `OperatingMode`  | `Running` or `Suspended`                                          |
| `createdBy`        | `CreatedBy`      | Who created it                                                    |
| `autoStopInterval` | `AutoStopInterval` | Minutes without a toolbox call until it stops; 0 never          |
| `lastActivityAt` *m* | `LastActivityAt` *m* | Last toolbox call the cluster saw; null before the first    |
| `persistent` *m*   | `Persistent` *m* | Has a volume of its own; a stop keeps what lies on it             |
| `data.kind`        | `Kind`           | `SandboxClaim` (warm pool) or `Sandbox` (own image)               |
| `data.ephemeral`   | `Ephemeral`      | Pod and storage go with the sandbox                               |
| `data`             | —                | Everything the platform returned, as `SandboxInfo`                 |

> [!NOTE]
> Fields reflect the last call. Reload them with `refreshData()` / `RefreshData(ctx)`.

## Start, stop, delete

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.stop(); // the pod goes, the volume stays; waits up to 60 s
await sandbox.start(); // back with the same files; waits up to 60 s
await sandbox.start(120); // custom wait in seconds

await sandbox.waitUntilStarted(60); // poll until started; throws on "error" or timeout
await sandbox.waitUntilStopped(60);

await sandbox.delete(); // or: await mogenius.delete(sandbox)

// the same through the client
await mogenius.stop(sandbox);
await mogenius.start(sandbox);
```

</details>

<details>
<summary><b>Go</b></summary>

```go
err := sandbox.Stop(ctx)                                      // waits up to 60 s
err = sandbox.Start(ctx)                                      // waits up to 60 s
err = sandbox.StartWithTimeout(ctx, 2*time.Minute)            // 0: as long as ctx allows
err = sandbox.StopWithTimeout(ctx, 2*time.Minute, false)      // force is accepted, no difference on Kubernetes
err = sandbox.WaitForStart(ctx, time.Minute)
err = sandbox.WaitForStop(ctx, time.Minute)
err = sandbox.Delete(ctx)
err = sandbox.DeleteWithTimeout(ctx, 30*time.Second)
```

</details>

## Labels and lifetime

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.setLabels({ project: 'reports', stage: 'done' }); // replaces your labels; platform labels stay
await sandbox.setAutoDeleteInterval(30); // 30 minutes from now
await sandbox.setTtl(30); // alias
await sandbox.setAutoDeleteInterval(0); // no deadline
```

</details>

<details>
<summary><b>Go</b></summary>

```go
err := sandbox.SetLabels(ctx, map[string]string{"project": "reports", "stage": "done"})
minutes := 30
err = sandbox.SetAutoDeleteInterval(ctx, &minutes)
err = sandbox.SetAutoDeleteInterval(ctx, nil) // no deadline
```

</details>

## Auto-stop

A sandbox with an auto-stop interval is stopped after that many minutes without a toolbox call: the pod goes, the
volume stays, and `start()` brings the same sandbox back with its files. Toolbox calls are commands, sessions, file
operations and open tunnels; a session command that is still running and an attached terminal keep the sandbox up
too. Reading the sandbox (`get`, `list`, `refreshData`) or changing labels and lifetime does not count. The cluster's
operator measures it, so it works while your program is not running. Only what lies on the volume (the profile's
`mountPath`) survives a stop; the home directory does not.

<details open>
<summary><b>TypeScript</b></summary>

```ts
const sandbox = await mogenius.create({ snapshot: 'default', autoStopInterval: 30 }); // stop after 30 idle minutes
await sandbox.setAutostopInterval(60); // change it; 0 turns it off
await sandbox.refreshActivity(); // counts as a call: 60 minutes from now
console.log(sandbox.autoStopInterval, sandbox.lastActivityAt);
```

</details>

<details>
<summary><b>Go</b></summary>

```go
idle := 30
sandbox, err := client.Create(ctx, types.SnapshotParams{
	SandboxBaseParams: types.SandboxBaseParams{AutoStopInterval: &idle},
	Snapshot:          "default",
})
hour := 60
err = sandbox.SetAutoStopInterval(ctx, &hour) // nil or 0 turns it off
err = sandbox.RefreshActivity(ctx)
fmt.Println(sandbox.AutoStopInterval, sandbox.LastActivityAt)
```

</details>

| Field              | Meaning                                                                  |
| ------------------ | ------------------------------------------------------------------------ |
| `autoStopInterval` | Minutes; 0 when the sandbox never stops on its own                       |
| `lastActivityAt`   | Written by the cluster at most once a minute; null before the first call |
| `persistent`       | Whether the profile gives the sandbox a volume; without one a stop keeps nothing |

A lifetime (`autoDeleteInterval`) still runs while the sandbox is stopped and deletes it at the deadline.

## Directories

Asked from the running sandbox:

<details open>
<summary><b>TypeScript</b></summary>

```ts
const home = await sandbox.getUserRootDir(); // e.g. /home/coder
const work = await sandbox.getWorkDir(); // e.g. /home/coder/project
```

</details>

<details>
<summary><b>Go</b></summary>

```go
home, err := sandbox.GetUserHomeDir(ctx)
work, err := sandbox.GetWorkingDir(ctx)
```

</details>

## Cleaning up

> [!TIP]
> Always delete what you create — or give it a lifetime, so a crashed program leaves nothing behind.

<details open>
<summary><b>TypeScript</b></summary>

```ts
const sandbox = await mogenius.create({ autoDeleteInterval: 30 });
try {
  // …
} finally {
  await mogenius.delete(sandbox);
}
```

</details>

<details>
<summary><b>Go</b></summary>

```go
thirty := 30
sandbox, err := client.Create(ctx, types.SnapshotParams{
	SandboxBaseParams: types.SandboxBaseParams{AutoDeleteInterval: &thirty},
})
if err != nil {
	return err
}
defer sandbox.Delete(context.Background())
```

</details>

---

<p align="center"><a href="concepts.md">← Concepts</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="commands.md">Commands and code →</a></p>
