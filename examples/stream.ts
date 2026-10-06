// Run with: set -a; . ./.env; set +a; node examples/stream.ts
// Streams a long command's output line by line (MOG-4691), then shows that
// leaving the loop early stops the command in the container.
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ labels: { example: 'stream' } });
console.log(`sandbox ${sandbox.id} is ${sandbox.state} in pod ${sandbox.podName}`);

const decoder = new TextDecoder();
try {
  // 1. stdout and stderr apart, live, exit code at the end
  console.log('--- for i in 1 2 3; do echo $i; echo "err $i" >&2; sleep 1; done');
  let started = Date.now();
  for await (const event of sandbox.process.executeCommandStream(
    'for i in 1 2 3; do echo $i; echo "err $i" >&2; sleep 1; done',
  )) {
    const at = `${((Date.now() - started) / 1000).toFixed(1)}s`;
    if (event.type === 'exit') {
      console.log(`[${at}] exit ${event.exitCode} truncated=${event.truncated} timedOut=${event.timedOut}`);
    } else {
      process.stdout.write(`[${at}] ${event.type}: ${decoder.decode(event.data)}`);
    }
  }

  // 2. a non-zero exit code is a result, not an error
  console.log('--- exit 7');
  for await (const event of sandbox.process.executeCommandStream('echo failing >&2; exit 7')) {
    if (event.type === 'exit') {
      console.log(`exit ${event.exitCode}`);
    }
  }

  // 3. the timeout stops the command
  console.log('--- sleep 30 with timeout 2');
  started = Date.now();
  for await (const event of sandbox.process.executeCommandStream('sleep 30; echo late', undefined, undefined, 2)) {
    if (event.type === 'exit') {
      console.log(
        `exit ${event.exitCode} timedOut=${event.timedOut} after ${((Date.now() - started) / 1000).toFixed(1)}s`,
      );
    }
  }

  // 4. leaving the loop stops the command: the marker file must not appear
  console.log('--- break out of a running command');
  for await (const event of sandbox.process.executeCommandStream(
    'echo started; sleep 4; touch /tmp/late-marker',
    undefined,
    undefined,
    60,
  )) {
    if (event.type === 'stdout') {
      console.log('got first output, leaving the loop');
      break;
    }
  }
  await new Promise((resolve) => setTimeout(resolve, 6000));
  const check = await sandbox.process.executeCommand(
    'test -e /tmp/late-marker && echo "marker exists" || echo "no marker: command was stopped"',
  );
  console.log(check.result.trim());
} finally {
  await mogenius.delete(sandbox);
  console.log('deleted');
}
