import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  DEFAULT_API_URL,
  DEFAULT_NAMESPACE,
  errorFromResponse,
  MogeniusConflictError,
  MogeniusError,
  MogeniusForbiddenError,
  MogeniusNotFoundError,
  MogeniusOperatorUpgradeRequiredError,
  MogeniusProcessExecutionTimeoutError,
  MogeniusTimeoutError,
  MogeniusUnauthorizedError,
  MogeniusValidationError,
  resolveConfig,
} from '../src/index.js';

describe('resolveConfig', () => {
  const saved = { ...process.env };
  beforeEach(() => {
    for (const key of Object.keys(process.env)) {
      if (key.startsWith('MOGENIUS_')) {
        delete process.env[key];
      }
    }
  });
  afterEach(() => {
    process.env = { ...saved };
  });

  it('requires a key and applies defaults', () => {
    expect(() => resolveConfig({})).toThrow(MogeniusError);
    const config = resolveConfig({ apiKey: 'k' });
    expect(config.apiUrl).toBe(DEFAULT_API_URL);
    expect(config.namespace).toBe(DEFAULT_NAMESPACE);
    expect(config.organizationId).toBeUndefined();
    expect(config.clusterId).toBeUndefined();
  });

  it('reads the environment and lets explicit values win', () => {
    process.env.MOGENIUS_API_KEY = 'env-key';
    process.env.MOGENIUS_API_URL = 'https://env.test/';
    process.env.MOGENIUS_ORGANIZATION_ID = 'org-env';
    process.env.MOGENIUS_CLUSTER_ID = 'cluster-env';
    process.env.MOGENIUS_SANDBOX_NAMESPACE = 'ns-env';

    const fromEnv = resolveConfig();
    expect(fromEnv).toMatchObject({
      apiKey: 'env-key',
      apiUrl: 'https://env.test',
      organizationId: 'org-env',
      clusterId: 'cluster-env',
      namespace: 'ns-env',
    });

    const explicit = resolveConfig({ apiKey: 'k', target: 'from-target', clusterId: 'c' });
    expect(explicit.namespace).toBe('from-target');
    expect(explicit.clusterId).toBe('c');
    expect(explicit.organizationId).toBe('org-env');
  });
});

describe('errorFromResponse', () => {
  it('prefers the error code over the status', () => {
    expect(errorFromResponse(400, { errorCode: 'SANDBOX_ENV_NOT_ALLOWED', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusValidationError,
    );
    expect(errorFromResponse(500, { errorCode: 'SANDBOX_NOT_FOUND', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusNotFoundError,
    );
    expect(errorFromResponse(409, { errorCode: 'SANDBOX_NOT_READY', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusConflictError,
    );
    expect(errorFromResponse(408, { errorCode: 'SANDBOX_EXEC_TIMEOUT', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusProcessExecutionTimeoutError,
    );
    expect(errorFromResponse(504, { errorCode: 'OPERATOR_TIMEOUT', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusTimeoutError,
    );
    expect(errorFromResponse(424, { errorCode: 'OPERATOR_UPGRADE_REQUIRED', message: 'x' }, 'f')).toBeInstanceOf(
      MogeniusOperatorUpgradeRequiredError,
    );
  });

  it('falls back to the status for errors without a code', () => {
    expect(errorFromResponse(401, { message: 'API key not found' }, 'f')).toBeInstanceOf(MogeniusUnauthorizedError);
    expect(errorFromResponse(403, 'forbidden', 'f')).toBeInstanceOf(MogeniusForbiddenError);
    expect(errorFromResponse(404, null, 'fallback')).toBeInstanceOf(MogeniusNotFoundError);
    expect(errorFromResponse(500, null, 'fallback').message).toBe('fallback');
  });

  it('joins Nest validation message arrays and keeps the details', () => {
    const err = errorFromResponse(
      400,
      { statusCode: 400, message: ['command should not be empty', 'timeout must be an integer'] },
      'f',
    );
    expect(err.message).toBe('command should not be empty, timeout must be an integer');
    expect(err.statusCode).toBe(400);
    expect(err.name).toBe('MogeniusValidationError');
  });

  // A process timeout is a timeout: `catch (e) { if (e instanceof MogeniusTimeoutError) }` covers both.
  it('keeps the Daytona error hierarchy', () => {
    const err = errorFromResponse(408, { errorCode: 'SANDBOX_EXEC_TIMEOUT', message: 'x' }, 'f');
    expect(err).toBeInstanceOf(MogeniusTimeoutError);
    expect(err).toBeInstanceOf(MogeniusError);
    expect(err).toBeInstanceOf(Error);
  });
});
