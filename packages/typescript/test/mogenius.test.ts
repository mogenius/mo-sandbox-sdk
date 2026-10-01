import { describe, expect, it } from 'vitest';
import {
  Mogenius,
  MogeniusNotFoundError,
  MogeniusTimeoutError,
  MogeniusUnsupportedError,
  Sandbox,
} from '../src/index.js';
import { CONFIG, fakeFetch, sandboxInfo } from './helpers.js';

describe('Mogenius.create', () => {
  it('claims from the default profile, waits on the platform and sends the platform headers', async () => {
    const { fetch, calls } = fakeFetch({ status: 201, body: sandboxInfo() });
    const mogenius = new Mogenius({ ...CONFIG, fetch });

    const sandbox = await mogenius.create();

    expect(sandbox).toBeInstanceOf(Sandbox);
    expect(sandbox.id).toBe('default-abc12');
    expect(sandbox.state).toBe('started');
    expect(sandbox.podName).toBe('default-k27tp');
    const call = calls[0]!;
    expect(call.method).toBe('POST');
    expect(call.url.toString()).toBe('https://api.test/sandbox/agent-sandbox');
    expect(call.headers).toMatchObject({
      authorization: 'Bearer mo_pat:user:secret',
      'organization-id': 'org-1',
      'cluster-id': 'cluster-1',
      'content-type': 'application/json',
    });
    expect(call.body).toEqual({ waitForStart: true, waitTimeoutSeconds: 60 });
  });

  // Daytona's parameter names travel through unchanged and land on the platform's fields.
  it('maps Daytona create params onto the platform request', async () => {
    const { fetch, calls } = fakeFetch({ status: 201, body: sandboxInfo({ kind: 'Sandbox', id: 'custom' }) });
    const mogenius = new Mogenius({ ...CONFIG, fetch, namespace: 'team-a' });

    await mogenius.create(
      {
        name: 'custom',
        snapshot: 'gpu',
        image: 'python:3.13',
        labels: { team: 'a' },
        envVars: { FOO: 'bar' },
        autoDeleteInterval: 30,
        ephemeral: false,
        language: 'typescript',
      },
      { timeout: 120 },
    );

    expect(calls[0]!.url.pathname).toBe('/sandbox/team-a');
    expect(calls[0]!.body).toEqual({
      name: 'custom',
      profile: 'gpu',
      image: 'python:3.13',
      labels: { team: 'a' },
      env: { FOO: 'bar' },
      ttlMinutes: 30,
      ephemeral: false,
      waitForStart: true,
      waitTimeoutSeconds: 120,
    });
  });

  it('does not wait with timeout 0 and returns the sandbox as created', async () => {
    const { fetch, calls } = fakeFetch({ status: 201, body: sandboxInfo({ state: 'creating', podName: null }) });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).create({}, { timeout: 0 });
    expect(sandbox.state).toBe('creating');
    expect(calls[0]!.body).toEqual({ waitForStart: false });
    expect(calls).toHaveLength(1);
  });

  it('keeps polling when the platform returned before the sandbox started', async () => {
    const { fetch, calls } = fakeFetch(
      { status: 201, body: sandboxInfo({ state: 'starting' }) },
      { body: sandboxInfo({ state: 'starting' }) },
      { body: sandboxInfo({ state: 'started' }) },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).create({}, { timeout: 310 });
    expect(sandbox.state).toBe('started');
    expect(calls.map((c) => c.method)).toEqual(['POST', 'GET', 'GET']);
  }, 10_000);

  it('turns the platform error body into the matching error class', async () => {
    const { fetch } = fakeFetch({
      status: 404,
      body: {
        statusCode: 404,
        errorCode: 'SANDBOX_PROFILE_NOT_FOUND',
        source: 'api',
        message: 'No sandbox profile "gpu"',
      },
    });
    const err = await new Mogenius({ ...CONFIG, fetch }).create({ snapshot: 'gpu' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MogeniusNotFoundError);
    expect((err as MogeniusNotFoundError).errorCode).toBe('SANDBOX_PROFILE_NOT_FOUND');
    expect((err as MogeniusNotFoundError).source).toBe('api');
    expect((err as Error).message).toContain('gpu');
  });
});

describe('Mogenius.get / list / findOne', () => {
  it('get reads one sandbox by id', async () => {
    const { fetch, calls } = fakeFetch({ body: sandboxInfo({ id: 'x' }) });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('x');
    expect(sandbox.id).toBe('x');
    expect(calls[0]!.url.pathname).toBe('/sandbox/agent-sandbox/x');
  });

  it('list encodes labels, state, limit and cursor as query parameters', async () => {
    const { fetch, calls } = fakeFetch({
      body: { items: [sandboxInfo(), sandboxInfo({ id: 'b' })], nextCursor: 'Yg', totalCount: 5 },
    });
    const page = await new Mogenius({ ...CONFIG, fetch }).list(
      { team: 'a', env: 'prod' },
      { limit: 2, cursor: 'YQ' },
      'started',
    );
    expect(page.items).toHaveLength(2);
    expect(page.items[0]).toBeInstanceOf(Sandbox);
    expect(page.nextCursor).toBe('Yg');
    expect(page.total).toBe(5);
    const query = calls[0]!.url.searchParams;
    expect(query.get('labels')).toBe('team=a,env=prod');
    expect(query.get('state')).toBe('started');
    expect(query.get('limit')).toBe('2');
    expect(query.get('cursor')).toBe('YQ');
  });

  it('findOne by labels returns the first match and fails cleanly on none', async () => {
    const found = fakeFetch({ body: { items: [sandboxInfo({ id: 'match' })], nextCursor: null, totalCount: 1 } });
    expect((await new Mogenius({ ...CONFIG, fetch: found.fetch }).findOne({ labels: { team: 'a' } })).id).toBe('match');
    expect(found.calls[0]!.url.searchParams.get('limit')).toBe('1');

    const none = fakeFetch({ body: { items: [], nextCursor: null, totalCount: 0 } });
    await expect(
      new Mogenius({ ...CONFIG, fetch: none.fetch }).findOne({ labels: { team: 'z' } }),
    ).rejects.toBeInstanceOf(MogeniusNotFoundError);
  });

  it('names what Kubernetes sandboxes do not offer', () => {
    const mogenius = new Mogenius({ ...CONFIG, fetch: fakeFetch({}).fetch });
    expect(() => mogenius.snapshot).toThrow(MogeniusUnsupportedError);
    expect(() => mogenius.volume).toThrow(MogeniusUnsupportedError);
  });
});

describe('Sandbox lifecycle', () => {
  it('stop and start post to the action routes and settle on the returned state', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: sandboxInfo({ state: 'stopped', operatingMode: 'Suspended' }) },
      { body: sandboxInfo({ state: 'started' }) },
    );
    const mogenius = new Mogenius({ ...CONFIG, fetch });
    const sandbox = await mogenius.get('default-abc12');

    await sandbox.stop();
    expect(sandbox.state).toBe('stopped');
    expect(calls[1]!.url.pathname).toBe('/sandbox/agent-sandbox/default-abc12/stop');

    await mogenius.start(sandbox);
    expect(sandbox.state).toBe('started');
    expect(calls[2]!.url.pathname).toBe('/sandbox/agent-sandbox/default-abc12/start');
  });

  it('waitUntilStarted gives up with a timeout error', async () => {
    const { fetch } = fakeFetch({ body: sandboxInfo({ state: 'starting' }) });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.waitUntilStarted(1)).rejects.toBeInstanceOf(MogeniusTimeoutError);
  }, 10_000);

  it('setLabels and setAutoDeleteInterval patch the sandbox', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: sandboxInfo({ labels: { team: 'b' } }) },
      { body: sandboxInfo({ expiresAt: '2026-10-01T13:00:00.000Z' }) },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    expect(await sandbox.setLabels({ team: 'b' })).toEqual({ team: 'b' });
    expect(calls[1]!.method).toBe('PATCH');
    expect(calls[1]!.body).toEqual({ labels: { team: 'b' } });

    await sandbox.setAutoDeleteInterval(60);
    expect(calls[2]!.body).toEqual({ ttlMinutes: 60 });
    expect(sandbox.expiresAt).toBe('2026-10-01T13:00:00.000Z');
  });

  it('delete sends DELETE and exposes Daytona field names', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo({ labels: { a: '1' } }) },
      { body: sandboxInfo({ state: 'destroying' }) },
    );
    const mogenius = new Mogenius({ ...CONFIG, fetch });
    const sandbox = await mogenius.get('default-abc12');
    expect(sandbox.snapshot).toBe('default');
    expect(sandbox.target).toBe('agent-sandbox');
    expect(sandbox.labels).toEqual({ a: '1' });
    expect(sandbox.errorReason).toBeNull();

    await mogenius.delete(sandbox);
    expect(calls[1]!.method).toBe('DELETE');
    expect(sandbox.state).toBe('destroying');
    await expect(sandbox.archive()).rejects.toBeInstanceOf(MogeniusUnsupportedError);
  });
});
