import { describe, expect, it } from 'vitest';
import { Mogenius, MogeniusError, MogeniusNotFoundError } from '../src/index.js';
import { CONFIG, fakeFetch, sandboxInfo } from './helpers.js';

const fileInfo = (overrides: Record<string, unknown> = {}) => ({
  name: 'a.py',
  path: '/home/coder/project/a.py',
  isDir: false,
  size: 12,
  modTime: '2026-10-01T10:00:00.000Z',
  mode: '0644',
  permissions: '-rw-r--r--',
  owner: '1000',
  group: '1000',
  mimeType: 'text/x-python',
  ...overrides,
});

const base = '/sandbox/agent-sandbox/default-abc12/toolbox/files';

describe('FileSystem', () => {
  it('listFiles and getFileDetails read through the toolbox routes', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: [fileInfo(), fileInfo({ name: 'src', isDir: true })] },
      { body: fileInfo() },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const list = await sandbox.fs.listFiles('src');
    expect(calls[1]!.url.pathname).toBe(base);
    expect(calls[1]!.url.searchParams.get('path')).toBe('src');
    expect(list).toHaveLength(2);
    expect(list[0]).toMatchObject({
      name: 'a.py',
      isDir: false,
      size: 12,
      mode: '0644',
      owner: '1000',
      mimeType: 'text/x-python',
    });

    const details = await sandbox.fs.getFileDetails('/home/coder/project/a.py');
    expect(calls[2]!.url.pathname).toBe(`${base}/info`);
    expect(details.permissions).toBe('-rw-r--r--');
  });

  it('listFiles without a path lists the working directory', async () => {
    const { fetch, calls } = fakeFetch({ body: sandboxInfo() }, { body: [] });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await sandbox.fs.listFiles();
    expect(calls[1]!.url.searchParams.has('path')).toBe(false);
  });

  it('uploadFile sends multipart with the destination name and the path query', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: [{ path: '/home/coder/project/notes.txt', success: true, error: null }] },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    await sandbox.fs.uploadFile('hello', 'notes.txt');

    const call = calls[1]!;
    expect(call.url.pathname).toBe(`${base}/upload`);
    expect(call.url.searchParams.get('path')).toBe('notes.txt');
    expect(call.headers['content-type']).toBeUndefined(); // FormData sets its own boundary header
    expect(call.form).toBeInstanceOf(FormData);
    const part = call.form!.get('file') as File;
    expect(part.name).toBe('notes.txt');
    expect(await part.text()).toBe('hello');
  });

  it('uploadFile surfaces a failed part as an error', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      { status: 201, body: [{ path: '/x', success: false, error: 'read-only file system' }] },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.fs.uploadFile(Buffer.from('x'), '/x')).rejects.toBeInstanceOf(MogeniusError);
  });

  it('uploadFiles names every part after its destination', async () => {
    const results = [
      { path: '/a', success: true, error: null },
      { path: '/b', success: false, error: 'nope' },
    ];
    const { fetch, calls } = fakeFetch({ body: sandboxInfo() }, { status: 201, body: results });
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const outcome = await sandbox.fs.uploadFiles([
      { source: 'A', destination: '/a' },
      { source: Buffer.from('B'), destination: '/b' },
    ]);

    expect(outcome).toEqual(results);
    const form = calls[1]!.form!;
    expect([...form.keys()]).toEqual(['/a', '/b']);
    expect((form.get('/b') as File).name).toBe('b');
    expect(calls[1]!.url.searchParams.has('path')).toBe(false);
  });

  it('downloadFile asks for a download link, then fetches it without auth headers', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: { url: 'https://api.test/storage/download/tok-1', expiresInSeconds: 60 } },
      { raw: Buffer.from('binary\u0000data'), contentType: 'application/octet-stream' },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    const data = await sandbox.fs.downloadFile('/etc/hostname');

    expect(calls[1]!.method).toBe('POST');
    expect(calls[1]!.url.pathname).toBe(`${base}/download-link`);
    expect(calls[1]!.url.searchParams.get('path')).toBe('/etc/hostname');
    expect(calls[1]!.headers.authorization).toBe('Bearer mo_pat:user:secret');

    expect(calls[2]!.method).toBe('GET');
    expect(calls[2]!.url.href).toBe('https://api.test/storage/download/tok-1');
    expect(calls[2]!.headers.authorization).toBeUndefined();
    expect(calls[2]!.headers['organization-id']).toBeUndefined();

    expect(Buffer.isBuffer(data)).toBe(true);
    expect(data.toString('latin1')).toBe('binary\u0000data');
  });

  it('downloadFileStream hands out the bytes as a stream', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      { body: { url: 'https://api.test/storage/download/tok-2', expiresInSeconds: 60 } },
      { raw: Buffer.from('chunked'), contentType: 'text/plain' },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    const stream = await sandbox.fs.downloadFileStream('notes.txt');
    expect(stream).toBeInstanceOf(ReadableStream);
    expect(await new Response(stream).text()).toBe('chunked');
  });

  it('downloadFile maps a missing file to the not-found error class before any link is fetched', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 404, body: { errorCode: 'SANDBOX_FILE_NOT_FOUND', message: 'No file at "/nope".' } },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.fs.downloadFile('/nope')).rejects.toBeInstanceOf(MogeniusNotFoundError);
    expect(calls).toHaveLength(2);
  });

  it('downloadFile maps a spent or expired link to the not-found error class', async () => {
    const { fetch } = fakeFetch(
      { body: sandboxInfo() },
      { body: { url: 'https://api.test/storage/download/tok-3', expiresInSeconds: 60 } },
      { status: 404, body: { errorCode: 'DOWNLOAD_LINK_INVALID', message: 'expired' } },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    await expect(sandbox.fs.downloadFile('a.txt')).rejects.toBeInstanceOf(MogeniusNotFoundError);
  });

  it('downloadFile falls back to the buffered route on a platform without download links', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 404, body: { statusCode: 404, error: 'Not Found', message: `Cannot POST ${base}/download-link` } },
      { raw: Buffer.from('old way'), contentType: 'application/octet-stream' },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');
    const data = await sandbox.fs.downloadFile('a.txt');
    expect(data.toString('utf8')).toBe('old way');
    expect(calls[2]!.method).toBe('GET');
    expect(calls[2]!.url.pathname).toBe(`${base}/download`);
  });

  it('createFolder, moveFiles, deleteFile and setFilePermissions carry their parameters as query', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { status: 204 },
      { status: 204 },
      { status: 204 },
      { body: fileInfo({ mode: '0600', permissions: '-rw-------', owner: 'root' }) },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    await sandbox.fs.createFolder('out', '755');
    expect(calls[1]!.method).toBe('POST');
    expect(calls[1]!.url.pathname).toBe(`${base}/folder`);
    expect(Object.fromEntries(calls[1]!.url.searchParams)).toEqual({ path: 'out', mode: '755' });

    await sandbox.fs.moveFiles('a.py', 'out/a.py');
    expect(Object.fromEntries(calls[2]!.url.searchParams)).toEqual({ source: 'a.py', destination: 'out/a.py' });

    await sandbox.fs.deleteFile('out', true);
    expect(calls[3]!.method).toBe('DELETE');
    expect(Object.fromEntries(calls[3]!.url.searchParams)).toEqual({ path: 'out', recursive: 'true' });

    const info = await sandbox.fs.setFilePermissions('a.py', { mode: '600', owner: 'root' });
    expect(Object.fromEntries(calls[4]!.url.searchParams)).toEqual({ path: 'a.py', mode: '600', owner: 'root' });
    expect(info.owner).toBe('root');
  });

  it('searchFiles, findFiles and replaceInFiles follow Daytona shapes', async () => {
    const { fetch, calls } = fakeFetch(
      { body: sandboxInfo() },
      { body: { files: ['/home/coder/project/a.py'], truncated: false } },
      { body: { matches: [{ file: '/home/coder/project/a.py', line: 3, content: 'import os' }], truncated: false } },
      { body: [{ file: '/home/coder/project/a.py', success: true, error: null }] },
    );
    const sandbox = await new Mogenius({ ...CONFIG, fetch }).get('default-abc12');

    const search = await sandbox.fs.searchFiles('.', '*.py');
    expect(calls[1]!.url.pathname).toBe(`${base}/search`);
    expect(calls[1]!.url.searchParams.get('pattern')).toBe('*.py');
    expect(search.files).toEqual(['/home/coder/project/a.py']);

    const matches = await sandbox.fs.findFiles('.', 'import');
    expect(calls[2]!.url.pathname).toBe(`${base}/find`);
    expect(matches).toEqual([{ file: '/home/coder/project/a.py', line: 3, content: 'import os' }]);

    const replaced = await sandbox.fs.replaceInFiles(['a.py'], 'os', 'sys');
    expect(calls[3]!.method).toBe('POST');
    expect(calls[3]!.body).toEqual({ files: ['a.py'], pattern: 'os', newValue: 'sys' });
    expect(replaced[0]!.success).toBe(true);
  });
});
