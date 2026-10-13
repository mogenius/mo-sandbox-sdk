# Errors

Every error the SDK throws (TS) or returns (Go) belongs to one hierarchy. Each carries:

| Field        | TypeScript   | Go           | Meaning                                                    |
| ------------ | ------------ | ------------ | ---------------------------------------------------------- |
| status code  | `statusCode` | `StatusCode` | HTTP status of the platform's answer                       |
| error code   | `errorCode`  | `ErrorCode`  | the platform's code, e.g. `SANDBOX_NOT_FOUND`              |
| source       | `source`     | `Source`     | who decided: `api`, `operator` (in your cluster) or `sdk`  |

> [!IMPORTANT]
> A non-zero exit code of a command is **not** an error — check `exitCode`.

## Kinds

| TypeScript                              | Go                                      | Status | When                                                           |
| --------------------------------------- | --------------------------------------- | ------ | -------------------------------------------------------------- |
| `MogeniusError`                         | `*MogeniusError`                        | any    | base of all; also no API key, no `fetch`                       |
| `MogeniusUnauthorizedError`             | `MogeniusAuthenticationError`           | 401    | the key was rejected                                           |
| `MogeniusForbiddenError`                | `MogeniusForbiddenError`                | 403    | no grant on workspace or cluster, RBAC, file permissions       |
| `MogeniusNotFoundError`                 | `MogeniusNotFoundError`                 | 404    | no sandbox, container, profile, file, session or command       |
| `MogeniusConflictError`                 | `MogeniusConflictError`                 | 409    | wrong state: not started, name taken, command still running    |
| `MogeniusValidationError`               | `MogeniusValidationError`               | 400    | bad parameter, label or env name; env not allowed by profile   |
| —                                       | `MogeniusRateLimitError`                | 429    | too many requests                                              |
| —                                       | `MogeniusServerError`                   | 5xx    | the platform failed                                            |
| `MogeniusTimeoutError`                  | `MogeniusTimeoutError`                  | 504    | a wait ran out (operator or the SDK's own deadline)            |
| `MogeniusProcessExecutionTimeoutError`  | `MogeniusProcessExecutionTimeoutError`  | 408    | the command ran past its timeout; is also a timeout error      |
| `MogeniusOperatorUpgradeRequiredError`  | `MogeniusOperatorUpgradeRequiredError`  | 424    | the cluster's operator is too old for this call                |
| `MogeniusUnsupportedError`              | `MogeniusUnsupportedError`              | —      | not available on Kubernetes pods; the message names the alternative |

### Error codes

| Code                                                                 | Kind                 |
| -------------------------------------------------------------------- | -------------------- |
| `SANDBOX_NOT_FOUND`, `CONTAINER_NOT_FOUND`, `SANDBOX_PROFILE_NOT_FOUND`, `FILE_NOT_FOUND`, `SESSION_NOT_FOUND`, `SESSION_COMMAND_NOT_FOUND` | not found |
| `SANDBOX_NOT_READY`, `SANDBOX_ALREADY_EXISTS`, `FILE_EXISTS`, `FILE_NOT_EMPTY`, `SESSION_ALREADY_EXISTS`, `SESSION_BUSY` | conflict |
| `FILE_PERMISSION_DENIED`                                             | forbidden            |
| `INVALID_REQUEST`, `SANDBOX_ENV_NOT_ALLOWED`                         | validation           |
| `EXEC_TIMEOUT`                                                       | process execution timeout |
| `OPERATOR_TIMEOUT`                                                   | timeout              |
| `OPERATOR_UPGRADE_REQUIRED`                                          | operator upgrade required |
| `UNSUPPORTED`                                                        | unsupported          |

## Handling errors

<details open>
<summary><b>TypeScript</b></summary>

```ts
import {
  MogeniusError,
  MogeniusNotFoundError,
  MogeniusConflictError,
  MogeniusProcessExecutionTimeoutError,
} from '@mogenius/sandbox';

try {
  const sandbox = await mogenius.get('report-42');
  await sandbox.process.executeCommand('make build', undefined, undefined, 60);
} catch (err) {
  if (err instanceof MogeniusNotFoundError) {
    console.log('gone — create a new one');
  } else if (err instanceof MogeniusConflictError && err.errorCode === 'SANDBOX_NOT_READY') {
    console.log('still starting, try again');
  } else if (err instanceof MogeniusProcessExecutionTimeoutError) {
    console.log('the build took too long');
  } else if (err instanceof MogeniusError) {
    console.error(err.statusCode, err.errorCode, err.source, err.message);
  } else {
    throw err;
  }
}
```

</details>

<details>
<summary><b>Go</b></summary>

The package `pkg/errors` shadows the standard `errors`; import it as `sdkerrors`. Every kind unwraps to
`*MogeniusError`. A cancelled context comes back as itself (`context.Canceled`).

```go
import (
	"errors"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
)

_, err := client.Get(ctx, "report-42")

var notFound *sdkerrors.MogeniusNotFoundError
var timeout *sdkerrors.MogeniusTimeoutError
var base *sdkerrors.MogeniusError
switch {
case errors.As(err, &notFound):
	fmt.Println("gone — create a new one")
case errors.As(err, &timeout):
	fmt.Println("a wait ran out")
case errors.As(err, &base):
	fmt.Println(base.StatusCode, base.ErrorCode, base.Source, base.Error())
case err != nil:
	return err
}
```

</details>

## Retry on a transient conflict

<details open>
<summary><b>TypeScript</b></summary>

```ts
async function withRetry<T>(fn: () => Promise<T>, attempts = 5): Promise<T> {
  for (let i = 1; ; i++) {
    try {
      return await fn();
    } catch (err) {
      const transient = err instanceof MogeniusConflictError && err.errorCode === 'SANDBOX_NOT_READY';
      if (!transient || i === attempts) throw err;
      await new Promise((r) => setTimeout(r, 1000 * i));
    }
  }
}

await withRetry(() => sandbox.process.executeCommand('echo ready'));
```

</details>

---

<p align="center"><a href="tunnels.md">← Tunnels</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="recipes.md">Recipes →</a></p>
