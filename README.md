<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img alt="mogenius" src="docs/assets/logo-light.svg" width="280">
  </picture>
</p>

<h1 align="center">mo-sandbox-sdk</h1>

<p align="center">
  <a href="packages/typescript"><img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-%40mogenius%2Fsandbox-3178C6?logo=typescript&logoColor=white"></a>
  <a href="packages/go"><img alt="Go" src="https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white"></a>
  <img alt="Node" src="https://img.shields.io/badge/Node-24%2B-5FA04E?logo=nodedotjs&logoColor=white">
  <img alt="Kubernetes" src="https://img.shields.io/badge/runs%20on-Kubernetes-326CE5?logo=kubernetes&logoColor=white">
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

<p align="center">
  <a href="docs/README.md"><b>📖 Documentation</b></a> ·
  <a href="docs/getting-started.md"><b>Getting started</b></a> ·
  <a href="docs/recipes.md"><b>Recipes</b></a> ·
  <a href="docs/api-reference.md"><b>API reference</b></a>
</p>


SDKs and API clients for **mogenius sandboxes**: isolated, short-lived environments for AI agents and code
execution, running as pods in your own Kubernetes cluster on top of
[kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox).

The SDK is built for drop-in use in agent frameworks: a client, a `Sandbox` with `process` and `fs`, and a
small error hierarchy keyed by status code.

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius(); // MOGENIUS_API_KEY, MOGENIUS_CLUSTER_ID, … from the environment
const sandbox = await mogenius.create(); // claimed from the warm pool, ready in seconds
const result = await sandbox.process.executeCommand('python3 --version');
console.log(result.exitCode, result.result);
await mogenius.delete(sandbox);
```

## Documentation

📖 **[Full documentation](docs/README.md)** — every function of both SDKs, with examples:
[Getting started](docs/getting-started.md) · [Concepts](docs/concepts.md) · [Sandboxes](docs/sandboxes.md) ·
[Commands and code](docs/commands.md) · [Sessions](docs/sessions.md) · [Files](docs/files.md) ·
[Tunnels](docs/tunnels.md) · [Errors](docs/errors.md) · [Recipes](docs/recipes.md) ·
[API reference](docs/api-reference.md)

## Packages

| Package                                      | Language   | Status                                                   |
| -------------------------------------------- | ---------- | -------------------------------------------------------- |
| [`@mogenius/sandbox`](packages/typescript)   | TypeScript | lifecycle, `executeCommand`, `codeRun` — in development  |
| [`…/mo-sandbox-sdk/packages/go`](packages/go) | Go         | the TypeScript SDK's surface in Go — in development      |
| `mogenius-sandbox`                           | Python     | planned                                                  |
| `openapi-specs/`                             | OpenAPI 3  | planned: generated clients for TypeScript, Python and Go |

## How it works

```
SDK ──HTTPS──► mogenius platform API ──► mogenius operator (in your cluster) ──exec──► sandbox pod
```

No agent inside the image, no open port on the pod: the operator executes commands through the Kubernetes
exec API under the caller's identity, every action is audited, and RBAC of the cluster applies. A sandbox is
a `SandboxClaim` against a warm pool (seconds) or a `Sandbox` object stamped from a profile's template with
your own image (a pod start).

## Development

```bash
npm install          # workspaces
npm run build        # every package
npm run test
npm run lint

cd packages/go && go test ./...   # the Go module
```

Examples live in [`examples/`](examples); copy `.env.example` to `.env`, load it into the shell (`set -a; . ./.env; set +a`) and run them with `node examples/<name>.ts` (Node 24 strips the types itself) or `npx tsx examples/<name>.ts`. `node --env-file=.env` works too, but it does not override a `MOGENIUS_*` variable that the shell already exports.

## License

Apache-2.0.
