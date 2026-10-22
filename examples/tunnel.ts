// Run with: set -a; . ./.env; set +a; node examples/tunnel.ts
// Starts an HTTP server in a sandbox and reaches it through a tunnel — once
// through the local port, once in-process with createConnection(). Needs
// MOGENIUS_ORGANIZATION_ID and MOGENIUS_CLUSTER_ID: tunnels go through the
// stream gateway.
import { request } from 'node:http';
import { Mogenius, type Tunnel } from '@mogenius/sandbox';

const mogenius = new Mogenius();
const sandbox = await mogenius.create({ labels: { example: 'tunnel' } }, { timeout: 120 });
console.log(`sandbox ${sandbox.id} is ${sandbox.state} in pod ${sandbox.podName}`);
if (sandbox.serviceFQDN) {
  console.log(`inside the cluster the sandbox is ${sandbox.serviceFQDN}`);
}

/** GET through the tunnel without the local port: the host is ignored. */
function getInProcess(tunnel: Tunnel, path: string): Promise<string> {
  return new Promise((resolve, reject) => {
    request({ host: 'sandbox', path, createConnection: () => tunnel.createConnection() }, (res) => {
      let body = '';
      res.on('data', (chunk: Buffer) => (body += chunk.toString()));
      res.on('end', () => resolve(`${res.statusCode} ${body.trim()}`));
    })
      .on('error', reject)
      .end();
  });
}

try {
  // a session keeps the server running after the call returns
  await sandbox.process.createSession('web');
  await sandbox.process.executeSessionCommand('web', {
    command: 'cd /tmp && echo tunnel-ok > index.html && python3 -m http.server 8000',
    runAsync: true,
  });
  await new Promise((resolve) => setTimeout(resolve, 2000));

  const tunnel = await sandbox.tunnel(8000);
  tunnel.done.then((error) => error && console.error('tunnel ended:', error.message));
  try {
    const response = await fetch(`${tunnel.url}/index.html`);
    console.log(`through ${tunnel.url}: ${response.status} ${(await response.text()).trim()}`);
    console.log(`through createConnection: ${await getInProcess(tunnel, '/index.html')}`);
  } finally {
    await tunnel.close();
  }
} finally {
  await mogenius.delete(sandbox);
  console.log('deleted');
}
