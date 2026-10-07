// Run with: set -a; . ./.env; set +a; node examples/sessions.ts
// Sessions (MOG-4692): one shell whose state carries over, background
// commands with live logs, input to an interactive command.
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ labels: { example: 'sessions' } });
console.log(`sandbox ${sandbox.id} is ${sandbox.state} in pod ${sandbox.podName}`);

try {
  const session = await sandbox.process.createSession('dev');
  console.log('session', session.sessionId, 'in container', session.container);

  // 1. state carries over
  await sandbox.process.executeSessionCommand('dev', { command: 'cd /tmp && export FOO=bar' });
  const state = await sandbox.process.executeSessionCommand('dev', { command: 'echo $FOO $PWD' });
  console.log('echo $FOO $PWD →', JSON.stringify(state.output), 'exit', state.exitCode);

  // 2. a background command, followed live, exit code afterwards
  const started = await sandbox.process.executeSessionCommand('dev', {
    command: 'for i in 1 2 3; do echo tick $i; echo "err $i" >&2; sleep 1; done; exit 4',
    runAsync: true,
  });
  console.log('started', started.cmdId, 'exitCode', started.exitCode);
  const begin = Date.now();
  await sandbox.process.getSessionCommandLogs('dev', started.cmdId, (chunk) =>
    process.stdout.write(`[${((Date.now() - begin) / 1000).toFixed(1)}s] ${chunk}`),
  );
  const finished = await sandbox.process.getSessionCommand('dev', started.cmdId);
  console.log('finished with exit', finished.exitCode);
  console.log('logs so far:', JSON.stringify(await sandbox.process.getSessionCommandLogs('dev', started.cmdId)));

  // 3. the session is gone after `exit`: open a fresh one for the input test
  const sessions = await sandbox.process.listSessions();
  console.log(
    'sessions after exit:',
    sessions.map((s) => s.sessionId),
  );
  await sandbox.process.createSession('input');
  const read = await sandbox.process.executeSessionCommand('input', {
    command: 'read name; echo hello $name',
    runAsync: true,
  });
  await new Promise((resolve) => setTimeout(resolve, 500));
  await sandbox.process.sendSessionCommandInput('input', read.cmdId, 'jane\n');
  const greeted = await new Promise<string>((resolve) => {
    let out = '';
    void sandbox.process.getSessionCommandLogs('input', read.cmdId, (chunk) => (out += chunk)).then(() => resolve(out));
  });
  console.log('read →', JSON.stringify(greeted));

  // 4. a synchronous wait that runs out leaves the command running
  const pending = await sandbox.process.executeSessionCommand('input', { command: 'sleep 3; echo late', timeout: 1 });
  console.log('after 1 s wait: exitCode', pending.exitCode, 'output', JSON.stringify(pending.output));
  await sandbox.process.getSessionCommandLogs('input', pending.cmdId, () => undefined);
  console.log('later:', (await sandbox.process.getSessionCommand('input', pending.cmdId)).exitCode);

  await sandbox.process.deleteSession('input');
  console.log(
    'sessions after delete:',
    (await sandbox.process.listSessions()).map((s) => s.sessionId),
  );
} finally {
  await mogenius.delete(sandbox);
  console.log('deleted');
}
