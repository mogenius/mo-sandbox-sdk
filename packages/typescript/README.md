# @mogenius/sandbox

TypeScript SDK for mogenius sandboxes with a Daytona-compatible surface.

```bash
npm install @mogenius/sandbox
```

## Configuration

| Option           | Environment variable        | Default                              |
| ---------------- | --------------------------- | ------------------------------------ |
| `apiKey`         | `MOGENIUS_API_KEY`          | required — an API key (`mo_pat:…`)   |
| `apiUrl`         | `MOGENIUS_API_URL`          | `https://platform-api.mogenius.com`  |
| `organizationId` | `MOGENIUS_ORGANIZATION_ID`  | taken from the key when it has one   |
| `clusterId`      | `MOGENIUS_CLUSTER_ID`       | taken from the key when it has one   |
| `namespace`      | `MOGENIUS_SANDBOX_NAMESPACE`| `agent-sandbox` (Daytona: `target`)  |

```ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius({ apiKey: process.env.MOGENIUS_API_KEY, clusterId: '…' });
```

## Sandboxes

```ts
// from the default profile's warm pool: bound and started in seconds
const sandbox = await mogenius.create({ labels: { project: 'demo' }, autoDeleteInterval: 60 });

// with your own image: a pod start from the profile's template
const custom = await mogenius.create({ image: 'python:3.13', envVars: { MODE: 'test' } }, { timeout: 120 });

const same = await mogenius.get(sandbox.id);
const found = await mogenius.findOne({ labels: { project: 'demo' } });
const page = await mogenius.list({ project: 'demo' }, { limit: 20 });

await sandbox.stop(); // pod goes, volume stays
await sandbox.start(); // back with the same files
await sandbox.setLabels({ project: 'demo', stage: 'done' });
await sandbox.setAutoDeleteInterval(30); // minutes from now; 0 removes the deadline
await mogenius.delete(sandbox);
```

## Running code

```ts
const shell = await sandbox.process.executeCommand('ls -la', '/home/coder/project', { DEBUG: '1' }, 30);
// shell.exitCode, shell.result (stdout + stderr), shell.artifacts.stdout / .stderr

const py = await sandbox.process.codeRun('print(sum(range(10)))');
const ts = await sandbox.process.codeRun('console.log(42)', { language: 'typescript' });
```

`executeCommand` is stateless: `cd` and `export` do not carry over. The default timeout is 10 seconds; the
cluster caps the maximum (300 seconds by default).

## Files

`sandbox.fs` is Daytona's `FileSystem`. Paths are absolute inside the container or relative to its working
directory (`/home/coder/project` in the default image).

```ts
await sandbox.fs.uploadFile(Buffer.from('print("hi")'), 'app.py');
await sandbox.fs.uploadFiles([
  { source: 'A=1', destination: 'config/.env' },
  { source: Buffer.from(bytes), destination: '/tmp/blob.bin' },
]);

const entries = await sandbox.fs.listFiles(); // FileInfo[]: name, isDir, size, modTime, mode, owner, group
const info = await sandbox.fs.getFileDetails('app.py');
const data = await sandbox.fs.downloadFile('app.py'); // Buffer; a folder comes as .tar.gz
const stream = await sandbox.fs.downloadFileStream('big.bin'); // mogenius only: ReadableStream for large files

await sandbox.fs.createFolder('out', '755');
await sandbox.fs.moveFiles('app.py', 'out/app.py');
await sandbox.fs.setFilePermissions('out/app.py', { mode: '600', owner: 'coder' });

const { files } = await sandbox.fs.searchFiles('.', '*.py'); // glob on names
const matches = await sandbox.fs.findFiles('.', 'import'); // grep: { file, line, content }[]
const results = await sandbox.fs.replaceInFiles(['out/app.py'], 'hi', 'hello'); // literal, per file

await sandbox.fs.deleteFile('out', true); // recursive; a non-empty folder without it is a conflict
```

The operator runs each operation inside the sandbox's container, so nothing is installed in the image for
it. Uploads are limited to 100 MB per file. `uploadFile` takes a Buffer or string, not a local path: read the
file yourself first.

## Errors

Every error extends `MogeniusError` and carries `statusCode`, `errorCode` and `source` (`api`, `operator`
or `sdk`). The hierarchy mirrors Daytona's:

| Class                                   | When                                                    |
| --------------------------------------- | ------------------------------------------------------- |
| `MogeniusNotFoundError`                 | sandbox, container or profile does not exist            |
| `MogeniusConflictError`                 | sandbox not ready, suspended, or the name is taken      |
| `MogeniusValidationError`               | bad label, env name or parameter                        |
| `MogeniusProcessExecutionTimeoutError`  | the command ran past its timeout (extends `…Timeout…`)  |
| `MogeniusTimeoutError`                  | a wait ran out                                          |
| `MogeniusForbiddenError`                | no grant on the workspace or cluster, or cluster RBAC   |
| `MogeniusOperatorUpgradeRequiredError`  | the cluster's operator is too old for this call         |
| `MogeniusUnsupportedError`              | Daytona has it, Kubernetes pods do not (fork, archive…) |

## Not available

Micro-VM features have no Kubernetes equivalent and throw `MogeniusUnsupportedError`: `fork`, `archive`,
`setAutostopInterval` (idle shutdown is planned), snapshot and volume management from the SDK, computer use.
