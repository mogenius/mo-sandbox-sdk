import type { ErrorBody } from './types.js';

/**
 * Base of every error the SDK throws. Carries what the platform said so a
 * caller can branch on `errorCode` or `statusCode` without parsing text.
 * One class per failure kind, all prefixed `Mogenius`.
 */
export class MogeniusError extends Error {
  readonly statusCode: number | undefined;
  readonly errorCode: string | undefined;
  /** Who decided: the platform API or the cluster's operator. */
  readonly source: 'api' | 'operator' | 'sdk';

  constructor(
    message: string,
    details: { statusCode?: number; errorCode?: string; source?: 'api' | 'operator' | 'sdk' } = {},
  ) {
    super(message);
    this.name = new.target.name;
    this.statusCode = details.statusCode;
    this.errorCode = details.errorCode;
    this.source = details.source ?? 'sdk';
  }
}

/** The key was rejected (401). */
export class MogeniusUnauthorizedError extends MogeniusError {}
/** The key may not do this (403): no EDITOR grant, no cluster role, or RBAC in the cluster. */
export class MogeniusForbiddenError extends MogeniusError {}
/** No sandbox, container or profile of that name (404). */
export class MogeniusNotFoundError extends MogeniusError {}
/** The sandbox is in the wrong state for this: not bound yet, suspended, name taken (409). */
export class MogeniusConflictError extends MogeniusError {}
/** The request itself is wrong (400): a bad label, env name, or parameter. */
export class MogeniusValidationError extends MogeniusError {}
/** A platform-side wait ran out (504 from the operator, or the SDK's own deadline). */
export class MogeniusTimeoutError extends MogeniusError {}
/** The command ran longer than its timeout and was stopped (408). */
export class MogeniusProcessExecutionTimeoutError extends MogeniusTimeoutError {}
/** The cluster's operator is too old for this call (424). */
export class MogeniusOperatorUpgradeRequiredError extends MogeniusError {}
/** Not available for Kubernetes sandboxes: fork, pause, archive, snapshots of running state, computer use. */
export class MogeniusUnsupportedError extends MogeniusError {
  constructor(feature: string, alternative?: string) {
    super(
      `${feature} is not supported by mogenius sandboxes (they are Kubernetes pods, not micro-VMs).` +
        (alternative ? ` ${alternative}` : ''),
      { errorCode: 'UNSUPPORTED', source: 'sdk' },
    );
  }
}

/** Builds the right error class from an HTTP failure of the platform API. */
export function errorFromResponse(status: number, body: ErrorBody | string | null, fallback: string): MogeniusError {
  const parsed: ErrorBody = typeof body === 'string' ? { message: body } : (body ?? {});
  const message = Array.isArray(parsed.message)
    ? parsed.message.join(', ')
    : parsed.message || parsed.error || fallback;
  const details = { statusCode: status, errorCode: parsed.errorCode, source: parsed.source };

  switch (parsed.errorCode) {
    case 'SANDBOX_EXEC_TIMEOUT':
      return new MogeniusProcessExecutionTimeoutError(message, details);
    case 'OPERATOR_TIMEOUT':
      return new MogeniusTimeoutError(message, details);
    case 'OPERATOR_UPGRADE_REQUIRED':
      return new MogeniusOperatorUpgradeRequiredError(message, details);
    case 'SANDBOX_NOT_FOUND':
    case 'SANDBOX_CONTAINER_NOT_FOUND':
    case 'SANDBOX_PROFILE_NOT_FOUND':
    case 'SANDBOX_FILE_NOT_FOUND':
      return new MogeniusNotFoundError(message, details);
    case 'SANDBOX_NOT_READY':
    case 'SANDBOX_ALREADY_EXISTS':
    case 'SANDBOX_FILE_EXISTS':
    case 'SANDBOX_FILE_NOT_EMPTY':
      return new MogeniusConflictError(message, details);
    case 'SANDBOX_FILE_PERMISSION_DENIED':
      return new MogeniusForbiddenError(message, details);
    case 'INVALID_REQUEST':
    case 'SANDBOX_ENV_NOT_ALLOWED':
      return new MogeniusValidationError(message, details);
    default:
      break;
  }
  switch (status) {
    case 400:
      return new MogeniusValidationError(message, details);
    case 401:
      return new MogeniusUnauthorizedError(message, details);
    case 403:
      return new MogeniusForbiddenError(message, details);
    case 404:
      return new MogeniusNotFoundError(message, details);
    case 408:
      return new MogeniusProcessExecutionTimeoutError(message, details);
    case 409:
      return new MogeniusConflictError(message, details);
    case 424:
      return new MogeniusOperatorUpgradeRequiredError(message, details);
    case 504:
      return new MogeniusTimeoutError(message, details);
    default:
      return new MogeniusError(message, details);
  }
}
