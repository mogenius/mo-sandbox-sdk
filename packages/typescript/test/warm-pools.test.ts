import { describe, expect, it } from 'vitest';
import { Mogenius, MogeniusError, MogeniusForbiddenError } from '../src/index.js';
import { CONFIG, fakeFetch } from './helpers.js';

function warmPool(name: string, replicas: number, ready: number, namespace = 'agent-sandbox') {
  return {
    apiVersion: 'extensions.agents.x-k8s.io/v1beta1',
    kind: 'SandboxWarmPool',
    metadata: { name, namespace },
    spec: { replicas, sandboxTemplateRef: { name: `${name}-template` } },
    status: { readyReplicas: ready },
  };
}

describe('Mogenius.warmPools', () => {
  it('lists the pools of the namespace from data.items, sorted by name', async () => {
    const { fetch, calls } = fakeFetch({
      body: { status: 'success', data: { kind: 'List', items: [warmPool('python', 2, 1), warmPool('default', 3, 3)] } },
    });
    const pools = await new Mogenius({ ...CONFIG, fetch }).warmPools();

    expect(pools).toEqual([
      { name: 'default', namespace: 'agent-sandbox', template: 'default-template', ready: 3, size: 3 },
      { name: 'python', namespace: 'agent-sandbox', template: 'python-template', ready: 1, size: 2 },
    ]);
    const call = calls[0]!;
    expect(call.method).toBe('GET');
    expect(call.url.pathname).toBe('/resource/workload/list');
    expect(Object.fromEntries(call.url.searchParams)).toEqual({
      kind: 'SandboxWarmPool',
      plural: 'sandboxwarmpools',
      apiVersion: 'extensions.agents.x-k8s.io/v1beta1',
      namespaced: 'true',
      namespace: 'agent-sandbox',
      withData: 'false',
    });
    expect(call.headers).toMatchObject({ 'organization-id': 'org-1', 'cluster-id': 'cluster-1' });
  });

  it('lists every namespace without the namespace parameter, sorted by namespace and name', async () => {
    const { fetch, calls } = fakeFetch({
      body: {
        status: 'success',
        data: {
          items: [
            warmPool('default', 1, 1, 'team-b'),
            warmPool('python', 2, 2),
            warmPool('gpu', 1, 0, 'team-a'),
            warmPool('default', 3, 3),
          ],
        },
      },
    });
    const pools = await new Mogenius({ ...CONFIG, fetch }).warmPools({ allNamespaces: true });

    expect(pools.map((pool) => `${pool.namespace}/${pool.name}`)).toEqual([
      'agent-sandbox/default',
      'agent-sandbox/python',
      'team-a/gpu',
      'team-b/default',
    ]);
    const params = calls[0]!.url.searchParams;
    expect(params.has('namespace')).toBe(false);
    expect(params.get('kind')).toBe('SandboxWarmPool');
    expect(params.get('withData')).toBe('false');
  });

  it('reads a bare list and fills missing fields with defaults', async () => {
    const { fetch } = fakeFetch({
      body: { status: 'success', data: [{ metadata: { name: 'fresh', namespace: 'agent-sandbox' } }] },
    });
    expect(await new Mogenius({ ...CONFIG, fetch }).warmPools()).toEqual([
      { name: 'fresh', namespace: 'agent-sandbox', template: '', ready: 0, size: 0 },
    ]);
  });

  it('answers an empty list when there is no data', async () => {
    const { fetch } = fakeFetch({ body: { status: 'success', data: null } });
    expect(await new Mogenius({ ...CONFIG, fetch }).warmPools()).toEqual([]);
  });

  it.each([
    ['operator 404', { body: { status: 'error', statusCode: 404, message: 'sandboxwarmpools not found' } }],
    ['operator text', { body: { status: 'error', message: 'the server could not find the requested resource' } }],
    [
      'api 400',
      {
        status: 400,
        body: { message: 'no matches for kind "SandboxWarmPool" in version "extensions.agents.x-k8s.io/v1beta1"' },
      },
    ],
    ['api 404', { status: 404, body: { message: 'not found' } }],
  ])('treats a cluster without the resource type as having no pools (%s)', async (_name, answer) => {
    const { fetch } = fakeFetch(answer);
    expect(await new Mogenius({ ...CONFIG, fetch }).warmPools()).toEqual([]);
  });

  it('passes real failures on', async () => {
    const forbidden = fakeFetch({ status: 403, body: { message: 'no access to this cluster' } });
    await expect(new Mogenius({ ...CONFIG, fetch: forbidden.fetch }).warmPools()).rejects.toBeInstanceOf(
      MogeniusForbiddenError,
    );

    const operator = fakeFetch({ body: { status: 'error', statusCode: 500, message: 'etcd timeout' } });
    const err = await new Mogenius({ ...CONFIG, fetch: operator.fetch }).warmPools().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(MogeniusError);
    expect(err).toMatchObject({ message: 'listing warm pools: etcd timeout', statusCode: 500, source: 'operator' });
  });
});
