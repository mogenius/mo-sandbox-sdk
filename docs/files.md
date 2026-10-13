# Files

`sandbox.fs` (TS) / `sandbox.FileSystem` (Go) is the file system of the sandbox's container. Paths are absolute
or relative to the container's working directory (`/home/coder/project` in the default image). The operator runs
each operation inside the container — nothing is installed in the image for it.

## Overview

| TypeScript                                   | Go                                                  | Does                                     |
| -------------------------------------------- | --------------------------------------------------- | ---------------------------------------- |
| `uploadFile(data, path)`                     | `UploadFile(ctx, source, path)`                     | write one file, creating parent folders  |
| `uploadFiles([{ source, destination }])`     | —                                                   | several files in one request             |
| —                                            | `UploadFileStream(ctx, reader, path, opts...)`      | upload without holding it in memory      |
| `downloadFile(path)`                         | `DownloadFile(ctx, path, localPath)`                | the file's bytes; a folder as `.tar.gz`  |
| `downloadFiles(paths)`                       | —                                                   | several files; one failure does not stop the rest |
| `downloadFileStream(path)`                   | `DownloadFileStream(ctx, path, opts...)`            | stream, resumes on its own               |
| `listFiles(path?)`                           | `ListFiles(ctx, path)`                              | entries directly below a folder          |
| `getFileDetails(path)`                       | `GetFileInfo(ctx, path)`                            | one entry                                |
| `createFolder(path, mode?)`                  | `CreateFolder(ctx, path, opts...)`                  | folder and its parents                   |
| `moveFiles(source, destination)`             | `MoveFiles(ctx, source, destination)`               | move or rename                           |
| `deleteFile(path, recursive?)`               | `DeleteFile(ctx, path, recursive)`                  | file or folder                           |
| `setFilePermissions(path, { mode, owner, group })` | `SetFilePermissions(ctx, path, opts...)`      | chmod / chown                            |
| `searchFiles(path, glob)`                    | `SearchFiles(ctx, path, glob)`                      | file names by shell glob                 |
| `findFiles(path, pattern)`                   | `FindFiles(ctx, path, pattern)`                     | lines by grep pattern                    |
| `replaceInFiles(files, pattern, newValue)`   | `ReplaceInFiles(ctx, files, pattern, newValue)`     | literal search & replace                 |

## Upload

```ts
import { readFile } from 'node:fs/promises';

// a string is written as UTF-8, a Buffer/Uint8Array as is
await sandbox.fs.uploadFile('print("hello")\n', 'app.py');
await sandbox.fs.uploadFile(await readFile('./data.csv'), '/home/coder/project/data/data.csv');

// several in one request: each failure is reported, the others still land
const results = await sandbox.fs.uploadFiles([
  { source: 'A=1\nB=2\n', destination: 'config/.env' },
  { source: await readFile('./model.bin'), destination: '/tmp/model.bin' },
]);
for (const r of results) if (!r.success) console.error(r.path, r.error);
```

`uploadFile` takes content, not a local path — read the file yourself. Limit: 100 MB per file.

```go
err := sandbox.FileSystem.UploadFile(ctx, []byte("print(\"hello\")\n"), "app.py")
err = sandbox.FileSystem.UploadFile(ctx, "./data.csv", "data/data.csv") // a string is a local path

// streamed: large or unknown size, with progress
file, _ := os.Open("./dataset.parquet")
defer file.Close()
err = sandbox.FileSystem.UploadFileStream(ctx, file, "/tmp/dataset.parquet",
	mogenius.WithUploadProgress(func(p mogenius.UploadProgress) { fmt.Println(p.BytesSent) }))
```

## Download

```ts
import { createWriteStream } from 'node:fs';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';

const data = await sandbox.fs.downloadFile('out/report.csv'); // Buffer
console.log(data.toString('utf8'));

const folder = await sandbox.fs.downloadFile('out'); // a folder comes as .tar.gz

const many = await sandbox.fs.downloadFiles(['a.txt', 'b.txt', 'missing.txt']);
for (const f of many) console.log(f.path, f.error ?? `${f.data!.length} bytes`);

// large files: a stream, never held in memory as a whole
const stream = await sandbox.fs.downloadFileStream('big.bin');
await pipeline(Readable.fromWeb(stream as any), createWriteStream('./big.bin'));
```

`downloadFileStream` (*mogenius addition*) resumes on its own: when the connection breaks, it asks for the rest
with a Range request, up to five times with growing pauses. What you read stays one complete file; if the file
changed in between, the stream fails instead of mixing versions.

```go
data, err := sandbox.FileSystem.DownloadFile(ctx, "out/report.csv", nil)

local := "./report.csv"
_, err = sandbox.FileSystem.DownloadFile(ctx, "out/report.csv", &local) // also writes it there

reader, err := sandbox.FileSystem.DownloadFileStream(ctx, "big.bin",
	mogenius.WithDownloadProgress(func(p mogenius.DownloadProgress) {
		fmt.Printf("%d / %d\n", p.BytesReceived, p.TotalBytes)
	}))
if err != nil {
	return err
}
defer reader.Close()
out, _ := os.Create("./big.bin")
defer out.Close()
_, err = io.Copy(out, reader)
```

## List and inspect

```ts
const entries = await sandbox.fs.listFiles(); // working directory
const tmp = await sandbox.fs.listFiles('/tmp');
for (const e of tmp) {
  console.log(e.isDir ? 'd' : '-', e.permissions, e.owner, e.size, e.name, e.mimeType ?? '');
}

const info = await sandbox.fs.getFileDetails('app.py');
console.log(info.path, info.mode, info.modTime);
```

```go
files, err := sandbox.FileSystem.ListFiles(ctx, "/tmp")
for _, f := range files {
	fmt.Println(f.IsDirectory, f.Permissions, f.Size, f.Name, f.ModifiedTime)
}
info, err := sandbox.FileSystem.GetFileInfo(ctx, "app.py")
```

`FileInfo`:

| TypeScript    | Go             | Meaning                                       |
| ------------- | -------------- | --------------------------------------------- |
| `name`        | `Name`         | base name                                     |
| `path`        | `Path`         | absolute path inside the container            |
| `isDir`       | `IsDirectory`  | folder or not                                 |
| `size`        | `Size`         | bytes                                         |
| `modTime`     | `ModifiedTime` | last change (ISO string / `time.Time`)        |
| `mode`        | `Mode`         | octal, e.g. `0644`                            |
| `permissions` | `Permissions`  | `ls -l` style, e.g. `-rw-r--r--`              |
| `owner`, `group` | `Owner`, `Group` | owner and group                         |
| `mimeType`    | `MimeType`     | sniffed media type of a regular file          |

## Create, move, delete, permissions

```ts
await sandbox.fs.createFolder('out/logs', '755'); // parents too; mode octal or symbolic
await sandbox.fs.moveFiles('app.py', 'out/app.py'); // the destination's folder must exist
await sandbox.fs.setFilePermissions('out/app.py', { mode: '600' });
await sandbox.fs.setFilePermissions('out', { owner: 'coder', group: 'coder' });
await sandbox.fs.setFilePermissions('run.sh', { mode: 'u+x' });

await sandbox.fs.deleteFile('out/app.py');
await sandbox.fs.deleteFile('out', true); // recursive; a non-empty folder without it is a conflict
```

```go
err := sandbox.FileSystem.CreateFolder(ctx, "out/logs", options.WithMode("755"))
err = sandbox.FileSystem.MoveFiles(ctx, "app.py", "out/app.py")
err = sandbox.FileSystem.SetFilePermissions(ctx, "out/app.py",
	options.WithPermissionMode("600"), options.WithOwner("coder"), options.WithGroup("coder"))
err = sandbox.FileSystem.DeleteFile(ctx, "out", true)
```

## Search and replace

```ts
// names by shell glob
const { files, truncated } = await sandbox.fs.searchFiles('.', '*.py');

// lines by grep pattern
const matches = await sandbox.fs.findFiles('src', 'TODO');
for (const m of matches) console.log(`${m.file}:${m.line}: ${m.content}`);

// literal replace, one result per file
const results = await sandbox.fs.replaceInFiles(['src/config.ts', 'src/app.ts'], 'localhost', '0.0.0.0');
for (const r of results) if (!r.success) console.error(r.file, r.error);
```

```go
found, err := sandbox.FileSystem.SearchFiles(ctx, ".", "*.py")
// found.(map[string]any): "files" []string, "truncated" bool

matches, err := sandbox.FileSystem.FindFiles(ctx, "src", "TODO")
for _, m := range matches.([]map[string]any) {
	fmt.Printf("%s:%d: %s\n", m["file"], m["line"], m["content"])
}

results, err := sandbox.FileSystem.ReplaceInFiles(ctx, []string{"src/config.ts"}, "localhost", "0.0.0.0")
```

## Errors you may see

| Error                     | Error code                    | When                                         |
| ------------------------- | ----------------------------- | -------------------------------------------- |
| `MogeniusNotFoundError`   | `FILE_NOT_FOUND`              | path does not exist                          |
| `MogeniusConflictError`   | `FILE_EXISTS`, `FILE_NOT_EMPTY` | target exists; folder not empty without `recursive` |
| `MogeniusForbiddenError`  | `FILE_PERMISSION_DENIED`      | the container user may not; or a viewer key writes |
