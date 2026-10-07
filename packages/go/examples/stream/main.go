// Command stream streams a long command's output as it is written, then
// shows that leaving the loop early stops the command in the container.
//
// Run from packages/go: set -a; . ../../.env; set +a; go run ./examples/stream
package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

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
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"example": "stream"}},
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

	// 1. stdout and stderr apart, live, the exit code at the end
	command := `for i in 1 2 3; do echo $i; echo "err $i" >&2; sleep 1; done`
	fmt.Println("---", command)
	started := time.Now()
	for event, err := range sandbox.Process.ExecuteCommandStream(ctx, command) {
		if err != nil {
			return err
		}
		at := time.Since(started).Round(100 * time.Millisecond)
		if event.Type == types.ExecEventExit {
			fmt.Printf("[%s] exit %d truncated=%t timedOut=%t\n", at, event.ExitCode, event.Truncated, event.TimedOut)
		} else {
			fmt.Printf("[%s] %s: %s", at, event.Type, event.Data)
		}
	}

	// 2. the timeout stops the command
	fmt.Println("--- sleep 30 with a timeout of 2 s")
	started = time.Now()
	for event, err := range sandbox.Process.ExecuteCommandStream(ctx, "sleep 30; echo late", options.WithExecuteTimeout(2*time.Second)) {
		if err != nil {
			return err
		}
		if event.Type == types.ExecEventExit {
			fmt.Printf("exit %d timedOut=%t after %s\n", event.ExitCode, event.TimedOut, time.Since(started).Round(100*time.Millisecond))
		}
	}

	// 3. leaving the loop stops the command: the marker file must not appear
	fmt.Println("--- break out of a running command")
	for event, err := range sandbox.Process.ExecuteCommandStream(ctx, "echo started; sleep 4; touch /tmp/late-marker",
		options.WithExecuteTimeout(60*time.Second)) {
		if err != nil {
			return err
		}
		if event.Type == types.ExecEventStdout {
			fmt.Println("got first output, leaving the loop")
			break
		}
	}
	time.Sleep(6 * time.Second)
	check, err := sandbox.Process.ExecuteCommand(ctx, `test -e /tmp/late-marker && echo "marker exists" || echo "no marker: command was stopped"`)
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimSpace(check.Result))
	return nil
}
