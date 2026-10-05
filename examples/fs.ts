// End-to-end check of `sandbox.fs` against a real cluster: creates a sandbox, runs every
// FileSystem call once, checks the answers, deletes the sandbox again.
//
// Run with (reads .env):
//   set -a; . ./.env; set +a; node examples/fs.ts   # Node 24 strips the types itself
//   npx tsx examples/fs.ts                          # any Node >= 20, after the same `set -a; . ./.env`
// `node --env-file=.env` also works, but it does NOT override a MOGENIUS_* variable that is
// already exported in the shell — an old key there wins and the platform answers 401.
//
// Optional: MOGENIUS_VIEWER_API_KEY — a key with the cluster-viewer role. Reads must pass
// with it, writes must be refused with 403.
import assert from 'node:assert/strict';
import {
  Mogenius,
  MogeniusConflictError,
  MogeniusError,
  MogeniusForbiddenError,
  MogeniusNotFoundError,
  type Sandbox,
} from '@mogenius/sandbox';

const failures: string[] = [];

async function step(name: string, run: () => Promise<void>): Promise<void> {
  const started = Date.now();
  try {
    await run();
    console.log(`  ok   ${name} (${Date.now() - started} ms)`);
  } catch (err) {
    failures.push(name);
    const detail = err instanceof MogeniusError ? ` [${err.name} ${err.statusCode ?? ''} ${err.errorCode ?? ''}]` : '';
    console.log(`  FAIL ${name}${detail}\n       ${(err as Error).message}`);
  }
}

async function expectError<T extends MogeniusError>(
  run: () => Promise<unknown>,
  type: new (...args: never[]) => T,
): Promise<T> {
  try {
    await run();
  } catch (err) {
    if (err instanceof type) {
      return err;
    }
    throw new Error(`expected ${type.name}, got ${(err as Error).name}: ${(err as Error).message}`);
  }
  throw new Error(`expected ${type.name}, but the call succeeded`);
}

const mogenius = new Mogenius();
console.log('creating sandbox …');
const sandbox = await mogenius.create({ labels: { example: 'fs' } });
console.log(`sandbox ${sandbox.id} is ${sandbox.state} in pod ${sandbox.podName}`);

// everything happens below one folder relative to the working directory
const root = `e2e-fs-${Date.now()}`;
const src = `${root}/src`;
const withNewline = 'hello world\n';
const withoutNewline = 'TODO without newline';
const mixed = 'ä€ first\r\nsecond\n\n';
const binary = Buffer.from(Array.from({ length: 256 }, (_, i) => i));

try {
  await step('createFolder with mode, listFiles shows it', async () => {
    await sandbox.fs.createFolder(src, '755');
    const entries = await sandbox.fs.listFiles(root);
    assert.equal(entries.length, 1);
    assert.equal(entries[0]!.name, 'src');
    assert.equal(entries[0]!.isDir, true);
    assert.ok(entries[0]!.mode.endsWith('755'), `mode ${entries[0]!.mode}`);
    assert.ok(entries[0]!.path.startsWith('/'), 'path is absolute');
  });

  await step('uploadFile + downloadFile keep the bytes (trailing newline)', async () => {
    await sandbox.fs.uploadFile(withNewline, `${src}/a.txt`);
    const back = await sandbox.fs.downloadFile(`${src}/a.txt`);
    assert.equal(back.toString('utf8'), withNewline);
  });

  await step('upload/download without trailing newline and with CRLF + unicode', async () => {
    await sandbox.fs.uploadFile(withoutNewline, `${src}/d.txt`);
    await sandbox.fs.uploadFile(mixed, `${src}/mixed.txt`);
    assert.equal((await sandbox.fs.downloadFile(`${src}/d.txt`)).toString('utf8'), withoutNewline);
    assert.equal((await sandbox.fs.downloadFile(`${src}/mixed.txt`)).toString('utf8'), mixed);
  });

  await step('upload/download binary (all 256 byte values)', async () => {
    await sandbox.fs.uploadFile(binary, `${src}/blob.bin`);
    const back = await sandbox.fs.downloadFile(`${src}/blob.bin`);
    assert.ok(back.equals(binary), `got ${back.length} bytes`);
  });

  await step('downloadFileStream streams the same bytes', async () => {
    const stream = await sandbox.fs.downloadFileStream(`${src}/blob.bin`);
    const chunks: Buffer[] = [];
    for await (const chunk of stream as unknown as AsyncIterable<Uint8Array>) {
      chunks.push(Buffer.from(chunk));
    }
    assert.ok(Buffer.concat(chunks).equals(binary), `streamed ${Buffer.concat(chunks).length} bytes`);
  });

  await step('getFileDetails', async () => {
    const info = await sandbox.fs.getFileDetails(`${src}/a.txt`);
    assert.equal(info.name, 'a.txt');
    assert.equal(info.isDir, false);
    assert.equal(info.size, Buffer.byteLength(withNewline));
    assert.ok(info.path.endsWith(`/${src}/a.txt`), info.path);
    assert.match(info.permissions, /^-[rwx-]{9}$/);
    assert.ok(info.modTime, 'modTime set');
  });

  await step('uploadFiles creates parent folders, one result per file', async () => {
    const results = await sandbox.fs.uploadFiles([
      { source: 'print("b")\n', destination: `${src}/b.py` },
      { source: 'TODO in notes\n', destination: `${root}/notes/c.md` },
    ]);
    assert.equal(results.length, 2);
    for (const result of results) {
      assert.equal(result.success, true, `${result.path}: ${result.error}`);
    }
    const notes = await sandbox.fs.listFiles(`${root}/notes`);
    assert.deepEqual(
      notes.map((entry) => entry.name),
      ['c.md'],
    );
  });

  await step('searchFiles by glob', async () => {
    const found = await sandbox.fs.searchFiles(root, '*.py');
    assert.equal(found.files.length, 1);
    assert.ok(found.files[0]!.endsWith('/b.py'), found.files[0]);
    assert.equal(found.truncated, false);
  });

  await step('findFiles by content', async () => {
    const matches = await sandbox.fs.findFiles(root, 'TODO');
    const files = matches.map((match) => match.file.split('/').pop()).sort();
    assert.deepEqual(files, ['c.md', 'd.txt']);
    for (const match of matches) {
      assert.equal(match.line, 1);
      assert.ok(match.content.includes('TODO'), match.content);
    }
  });

  await step('replaceInFiles keeps line endings (with and without trailing newline)', async () => {
    const results = await sandbox.fs.replaceInFiles([`${root}/notes/c.md`, `${src}/d.txt`], 'TODO', 'DONE');
    assert.equal(results.length, 2);
    for (const result of results) {
      assert.equal(result.success, true, `${result.file}: ${result.error}`);
    }
    assert.equal((await sandbox.fs.downloadFile(`${root}/notes/c.md`)).toString('utf8'), 'DONE in notes\n');
    assert.equal((await sandbox.fs.downloadFile(`${src}/d.txt`)).toString('utf8'), 'DONE without newline');
    assert.equal((await sandbox.fs.findFiles(root, 'TODO')).length, 0);
  });

  await step('replaceInFiles reports a missing file without failing the others', async () => {
    const results = await sandbox.fs.replaceInFiles([`${src}/missing.txt`, `${src}/b.py`], 'print', 'print');
    const missing = results.find((result) => result.file.endsWith('missing.txt'));
    const present = results.find((result) => result.file.endsWith('b.py'));
    assert.ok(missing && !missing.success && missing.error, 'missing file reported');
    assert.ok(present?.success, 'present file replaced');
  });

  await step('setFilePermissions mode', async () => {
    const info = await sandbox.fs.setFilePermissions(`${src}/a.txt`, { mode: '600' });
    assert.ok(info.mode.endsWith('600'), info.mode);
    assert.equal(info.permissions, '-rw-------');
    const again = await sandbox.fs.getFileDetails(`${src}/a.txt`);
    assert.equal(again.permissions, '-rw-------');
  });

  await step('moveFiles renames, old path is gone', async () => {
    await sandbox.fs.moveFiles(`${src}/a.txt`, `${src}/renamed.txt`);
    const info = await sandbox.fs.getFileDetails(`${src}/renamed.txt`);
    assert.equal(info.size, Buffer.byteLength(withNewline));
    await expectError(() => sandbox.fs.getFileDetails(`${src}/a.txt`), MogeniusNotFoundError);
  });

  await step('downloadFiles collects data and errors', async () => {
    const results = await sandbox.fs.downloadFiles([`${src}/renamed.txt`, `${src}/nope.txt`]);
    assert.equal(results[0]!.data?.toString('utf8'), withNewline);
    assert.equal(results[0]!.error, undefined);
    assert.equal(results[1]!.data, undefined);
    assert.ok(results[1]!.error, 'error for the missing file');
  });

  await step('downloadFile of a folder is a gzipped tar', async () => {
    const archive = await sandbox.fs.downloadFile(src);
    assert.ok(archive.length > 20, `archive has ${archive.length} bytes`);
    assert.equal(archive[0], 0x1f);
    assert.equal(archive[1], 0x8b);
  });

  await step('deleteFile refuses a non-empty folder without recursive', async () => {
    await expectError(() => sandbox.fs.deleteFile(src), MogeniusConflictError);
    assert.ok((await sandbox.fs.listFiles(src)).length > 0, 'folder still there');
  });

  await step('deleteFile of a missing path is not found', async () => {
    await expectError(() => sandbox.fs.deleteFile(`${src}/nope.txt`), MogeniusNotFoundError);
  });

  await step('file names with spaces, quotes and a newline', async () => {
    const weird = `${root}/weird`;
    const names = ['with space.txt', `quotes "double" 'single'.txt`, 'line1\nline2.txt'];
    for (const name of names) {
      await sandbox.fs.uploadFile(`content of ${name}`, `${weird}/${name}`);
    }
    const listed = (await sandbox.fs.listFiles(weird)).map((entry) => entry.name).sort();
    assert.deepEqual(listed, [...names].sort());
    for (const name of names) {
      const info = await sandbox.fs.getFileDetails(`${weird}/${name}`);
      assert.equal(info.name, name);
      assert.equal((await sandbox.fs.downloadFile(`${weird}/${name}`)).toString('utf8'), `content of ${name}`);
    }
    assert.equal((await sandbox.fs.searchFiles(weird, '*.txt')).files.length, 3);
    assert.equal((await sandbox.fs.findFiles(weird, 'content of')).length, 3);
    await sandbox.fs.moveFiles(`${weird}/${names[0]}`, `${weird}/renamed with space.txt`);
    await sandbox.fs.deleteFile(`${weird}/${names[2]}`);
    const after = (await sandbox.fs.listFiles(weird)).map((entry) => entry.name).sort();
    assert.deepEqual(after, [names[1]!, 'renamed with space.txt'].sort());
  });

  await step('deleteFile recursive removes the tree', async () => {
    await sandbox.fs.deleteFile(root, true);
    await expectError(() => sandbox.fs.getFileDetails(root), MogeniusNotFoundError);
    const top = await sandbox.fs.listFiles();
    assert.ok(!top.some((entry) => entry.name === root), 'root folder gone from the working directory');
  });

  const viewerKey = process.env.MOGENIUS_VIEWER_API_KEY;
  if (viewerKey) {
    console.log('viewer key set: reads must pass, writes must be 403');
    let viewer: Sandbox | undefined;
    await step('viewer: get sandbox and read files', async () => {
      viewer = await new Mogenius({ apiKey: viewerKey }).get(sandbox.id);
      await sandbox.fs.uploadFile(withNewline, `${root}/viewer.txt`);
      const entries = await viewer.fs.listFiles(root);
      assert.deepEqual(
        entries.map((entry) => entry.name),
        ['viewer.txt'],
      );
      await viewer.fs.getFileDetails(`${root}/viewer.txt`);
      assert.equal((await viewer.fs.downloadFile(`${root}/viewer.txt`)).toString('utf8'), withNewline);
      assert.equal((await viewer.fs.searchFiles(root, '*.txt')).files.length, 1);
      assert.equal((await viewer.fs.findFiles(root, 'hello')).length, 1);
    });
    await step('viewer: every write is forbidden', async () => {
      assert.ok(viewer, 'viewer sandbox loaded');
      const writes: [string, () => Promise<unknown>][] = [
        ['createFolder', () => viewer!.fs.createFolder(`${root}/x`)],
        ['uploadFile', () => viewer!.fs.uploadFile('x', `${root}/x.txt`)],
        ['uploadFiles', () => viewer!.fs.uploadFiles([{ source: 'x', destination: `${root}/y.txt` }])],
        ['moveFiles', () => viewer!.fs.moveFiles(`${root}/viewer.txt`, `${root}/moved.txt`)],
        ['setFilePermissions', () => viewer!.fs.setFilePermissions(`${root}/viewer.txt`, { mode: '600' })],
        ['replaceInFiles', () => viewer!.fs.replaceInFiles([`${root}/viewer.txt`], 'hello', 'bye')],
        ['deleteFile', () => viewer!.fs.deleteFile(`${root}/viewer.txt`)],
      ];
      for (const [name, write] of writes) {
        const err = await expectError(write, MogeniusForbiddenError);
        assert.equal(err.statusCode, 403, `${name}: status ${err.statusCode}`);
      }
      // nothing changed
      assert.equal((await sandbox.fs.downloadFile(`${root}/viewer.txt`)).toString('utf8'), withNewline);
      await sandbox.fs.deleteFile(root, true);
    });
  } else {
    console.log('MOGENIUS_VIEWER_API_KEY not set: viewer checks skipped');
  }
} finally {
  await mogenius.delete(sandbox);
  console.log(`sandbox ${sandbox.id} deleted`);
}

if (failures.length > 0) {
  console.log(`\n${failures.length} step(s) failed:\n  - ${failures.join('\n  - ')}`);
  process.exit(1);
}
console.log('\nall fs checks passed');
