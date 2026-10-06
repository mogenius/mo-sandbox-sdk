// Run with: MOGENIUS_API_KEY=mo_pat:... npx tsx examples/basic.ts
import { Mogenius } from '@mogenius/sandbox';

const mogenius = new Mogenius();

const sandbox = await mogenius.create({ labels: { example: 'basic' } });
console.log(`sandbox ${sandbox.id} is ${sandbox.state} in pod ${sandbox.podName}`);

const shell = await sandbox.process.executeCommand('echo hello from $(hostname); uname -a');
console.log(shell.exitCode, shell.result);

const python = await sandbox.process.codeRun('import sys\nprint("python", sys.version.split()[0])');
console.log(python.result);

const typescript = await sandbox.process.codeRun('console.log("typescript", process.version)', {
  language: 'typescript',
});
console.log(typescript.result);

console.log('home:', await sandbox.getUserRootDir(), 'workdir:', await sandbox.getWorkDir());

await mogenius.delete(sandbox);
console.log('deleted');
