# Tunnels

*mogenius addition.* A tunnel forwards TCP connections from a local port to a port inside the sandbox, like
`kubectl port-forward` — through the platform's stream gateway, so the cluster needs no ingress. HTTP,
WebSockets, gRPC and plain TCP (a database) all pass. Only bytes travel: the sandbox never sees your credentials.

> [!NOTE]
> Needs `organizationId` and `clusterId` in the config. TypeScript: Node only (the local side is a TCP server).

> [!TIP]
> Inside the cluster you do not need a tunnel: reach the sandbox at `sandbox.serviceFQDN`.

## Open a tunnel

Start the server inside the sandbox in a [session](sessions.md), so it keeps running after the call returns:

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.process.createSession('web');
await sandbox.process.executeSessionCommand('web', {
  command: 'cd /tmp && echo ok > index.html && python3 -m http.server 8000',
  runAsync: true,
});

const tunnel = await sandbox.tunnel(8000); // a free local port
// const tunnel = await sandbox.tunnel(8000, { localPort: 3000 }); // a fixed one
try {
  console.log(tunnel.url); // http://127.0.0.1:54321
  const response = await fetch(`${tunnel.url}/index.html`);
  console.log(response.status, await response.text()); // 200 ok
} finally {
  await tunnel.close();
}
```

</details>

`Tunnel` has `host` (`127.0.0.1`), `port`, `url`, `createConnection()`, `close()` and `done` — a promise that
settles when the tunnel ends: with the error that ended it, or `undefined` after `close()`.

`createConnection()` opens a connection to the sandbox's port inside your process, without the local port — for
`http.request`, a WebSocket or a database client that takes a socket. It behaves like a TCP socket: ending or
destroying one side ends the other. It throws while the tunnel is reconnecting and after it ended.

<details open>
<summary><b>TypeScript</b></summary>

```ts
import { request } from 'node:http';

tunnel.done.then((error) => {
  if (error) console.error('tunnel ended:', error.message);
});

// in-process, without the local port: the host is ignored
request({ host: 'sandbox', path: '/index.html', createConnection: () => tunnel.createConnection() }, (res) => {
  res.pipe(process.stdout);
}).end();
```

</details>

<details>
<summary><b>Go</b></summary>

```go
tunnel, err := sandbox.Tunnel(ctx, 8000) // options.WithLocalPort(3000) fixes the local port
if err != nil {
	return err
}
defer tunnel.Close()

fmt.Println(tunnel.Addr()) // 127.0.0.1:54321
response, err := http.Get(tunnel.URL() + "/index.html")

// in-process, without a local port: an http.Transport, a gRPC dialer, a WebSocket client
client := &http.Client{Transport: &http.Transport{DialContext: tunnel.DialContext}}
response, err = client.Get("http://sandbox/index.html") // host is ignored

// watch for the end
go func() {
	<-tunnel.Done()
	if err := tunnel.Err(); err != nil {
		log.Println("tunnel ended:", err)
	}
}()
```

`ctx` bounds only the setup; the tunnel lives until `Close`.

</details>

## Behaviour

- **Reconnects on its own.** When the gateway connection drops, the tunnel reconnects with growing pauses;
  connections open at that moment break, new ones work again.
- **Ends when the pod is gone** — the sandbox was stopped or deleted. `done` / `Done()` and `Err()` say why.
- **Several connections at once.** Each local TCP connection becomes its own stream through the tunnel.

## Examples

A database in the sandbox, reached from a local client:

<details open>
<summary><b>TypeScript</b></summary>

```ts
await sandbox.process.createSession('db');
await sandbox.process.executeSessionCommand('db', { command: 'redis-server --port 6379', runAsync: true });
const tunnel = await sandbox.tunnel(6379, { localPort: 6379 });
// redis-cli -p 6379 ping  →  PONG
```

</details>

A preview of a web app an agent built — see [Recipes](recipes.md#preview-a-web-app).

---

<p align="center"><a href="files.md">← Files</a> &nbsp;·&nbsp; <a href="README.md">Overview</a> &nbsp;·&nbsp; <a href="errors.md">Errors →</a></p>
