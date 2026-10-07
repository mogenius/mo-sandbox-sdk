// Command basic creates a sandbox, runs a shell command and code in it, and
// deletes it again.
//
// Run from packages/go: set -a; . ../../.env; set +a; go run ./examples/basic
package main

import (
	"context"
	"fmt"
	"log"

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
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"example": "basic"}},
	})
	if err != nil {
		return err
	}
	fmt.Printf("sandbox %s is %s in pod %s\n", sandbox.ID, sandbox.State, *sandbox.PodName)
	defer func() {
		if deleteErr := sandbox.Delete(ctx); deleteErr != nil && err == nil {
			err = deleteErr
		}
		fmt.Println("deleted")
	}()

	shell, err := sandbox.Process.ExecuteCommand(ctx, "echo hello from $(hostname); uname -a")
	if err != nil {
		return err
	}
	fmt.Print(shell.ExitCode, " ", shell.Result)

	python, err := sandbox.Process.CodeRun(ctx, "import sys\nprint(\"python\", sys.version.split()[0])")
	if err != nil {
		return err
	}
	fmt.Print(python.Result)

	typescript, err := sandbox.Process.CodeRun(ctx, `console.log("typescript", process.version)`,
		options.WithCodeRunLanguage(types.CodeLanguageTypeScript))
	if err != nil {
		return err
	}
	fmt.Print(typescript.Result)

	home, err := sandbox.GetUserHomeDir(ctx)
	if err != nil {
		return err
	}
	workdir, err := sandbox.GetWorkingDir(ctx)
	if err != nil {
		return err
	}
	fmt.Println("home:", home, "workdir:", workdir)
	return nil
}
