// Run with: set -a; . ./.env; set +a; node examples/lifecycle.ts
// The life of a sandbox: the warm pools it can come from, create with labels,
// a lifetime and auto-stop, find and list it, change labels and auto-stop,
// stop and start it, delete it.
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();

// 1. where sandboxes come from: the warm pools of the namespace, then of every namespace
const pools = await mogenius.warmPools();
console.log(`warm pools in the namespace: ${pools.length}`);
for (const pool of await mogenius.warmPools({ allNamespaces: true })) {
  console.log(`  ${pool.namespace}/${pool.name} (template ${pool.template}): ${pool.ready}/${pool.size} ready`);
}

// 2. claimed from the configured profile's pool; deleted after 30 minutes at the latest
const run = `${Date.now()}`;
const sandbox = await mogenius.create({
  labels: { example: 'lifecycle', run },
  autoDeleteInterval: 30,
  autoStopInterval: 10,
});
console.log(`sandbox ${sandbox.id} is ${sandbox.state}, expires ${sandbox.expiresAt}`);
console.log(`  auto-stop after ${sandbox.autoStopInterval} idle minutes, persistent volume: ${sandbox.persistent}`);

try {
  // 3. find it again: by labels, by id, and in a list page
  const found = await mogenius.findOne({ labels: { run }, state: 'started' });
  console.log(`findOne by labels: ${found.id}`);
  console.log(`findOne by id: ${(await mogenius.findOne({ id: sandbox.id })).id}`);
  const page = await mogenius.list({ example: 'lifecycle' }, { limit: 10 });
  console.log(`list: ${page.items.length} of ${page.total} with example=lifecycle, more: ${page.nextCursor !== null}`);

  // 4. labels and auto-stop change in place; refreshActivity counts as a toolbox call
  await sandbox.setLabels({ example: 'lifecycle', run, stage: 'changed' });
  await sandbox.setAutostopInterval(20);
  await sandbox.refreshActivity();
  await sandbox.refreshData();
  console.log(`labels ${JSON.stringify(sandbox.labels)}`);
  console.log(`auto-stop ${sandbox.autoStopInterval} min, last activity ${sandbox.lastActivityAt}`);

  // 5. stop: the pod goes, the volume stays; start brings the same sandbox back
  await sandbox.stop();
  console.log(`stopped: ${sandbox.state}, pod ${sandbox.podName}`);
  await sandbox.start();
  await sandbox.refreshData();
  console.log(`started again: ${sandbox.state} in pod ${sandbox.podName}`);
} finally {
  await mogenius.delete(sandbox);
  console.log('deleted');
}
