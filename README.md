# mo-sandbox-sdk

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

Examples live in [`examples/`](examples); copy `.env.example` to `.env`, load it into the shell (`set -a; . ./.env; set +a`) and run them with `node examples/<name>.ts` (Node 24) or `npx tsx examples/<name>.ts`. `node --env-file=.env` works too, but it does not override a `MOGENIUS_*` variable that the shell already exports.

## License

Apache-2.0.
