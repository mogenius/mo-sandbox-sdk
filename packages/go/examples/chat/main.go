// Command chat holds a conversation with Claude Code in a sandbox: one
// `claude` process reads the messages from its stdin for the whole
// conversation, and every turn takes as long as it takes — a session command
// has no time limit of its own. The second turn runs longer than the
// cluster's limit for a plain command (5 minutes by default). Afterwards the
// session is deleted, which stops claude, and a new session resumes the
// conversation from the sandbox's disk.
//
// The sandbox profile needs `claude` and its provider key (a warm pool that
// sets ANTHROPIC_API_KEY): MOGENIUS_SANDBOX_PROFILE=platform-test. Variables
// for the agent travel with the session (CHAT_EXAMPLE_TOKEN here), never in
// a message or a command.
//
// Run from packages/go: set -a; . ../../.env; set +a; go run ./examples/chat [-long 6m]
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/mogenius"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

const claudeCommand = "claude -p --input-format stream-json --output-format stream-json --verbose --permission-mode bypassPermissions"

func main() {
	long := flag.Duration("long", 6*time.Minute, "how long the second turn should keep claude busy")
	flag.Parse()
	if err := run(context.Background(), *long); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, long time.Duration) (err error) {
	client, err := mogenius.NewClient()
	if err != nil {
		return err
	}
	sandbox, err := client.Create(ctx, types.SnapshotParams{
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"example": "chat"}},
	})
	if err != nil {
		return err
	}
	fmt.Printf("sandbox %s in pod %s\n", sandbox.ID, *sandbox.PodName)
	defer func() {
		if deleteErr := sandbox.Delete(ctx); deleteErr != nil && err == nil {
			err = deleteErr
		}
		fmt.Println("deleted")
	}()

	process := sandbox.Process
	env := map[string]string{"CHAT_EXAMPLE_TOKEN": "made-up-" + sandbox.ID}
	if err := process.CreateSession(ctx, "chat", options.WithSessionEnv(env)); err != nil {
		return err
	}
	started, err := process.ExecuteSessionCommand(ctx, "chat", claudeCommand, true, false)
	if err != nil {
		return err
	}
	cmdID := started["id"].(string)
	events := follow(ctx, process, "chat", cmdID)

	turns := []string{
		"Remember the word 'tangerine'. Answer with one short sentence.",
		fmt.Sprintf("Use the Bash tool to run `date` once a minute for %d minutes (each call: `date; sleep 60`). "+
			"Then tell me how many lines you printed.", int(long.Minutes())),
		"Is the variable CHAT_EXAMPLE_TOKEN set in your environment? Check with Bash and answer yes or no, without its value.",
	}
	var claudeSession string
	for i, text := range turns {
		begin := time.Now()
		fmt.Printf("\n--- turn %d: %s\n", i+1, text)
		if err := process.SendSessionCommandInput(ctx, "chat", cmdID, userMessage(text)); err != nil {
			return err
		}
		result, id, err := awaitResult(events)
		if err != nil {
			return err
		}
		if id != "" {
			claudeSession = id
		}
		fmt.Printf("--- turn %d done after %s: %s\n", i+1, time.Since(begin).Round(time.Second), result)
	}

	// Deleting the session stops claude; the conversation stays on the
	// sandbox's disk, and --resume picks it up in a new session.
	if err := process.DeleteSession(ctx, "chat"); err != nil {
		return err
	}
	if err := process.CreateSession(ctx, "resume", options.WithSessionEnv(env)); err != nil {
		return err
	}
	resumed, err := process.ExecuteSessionCommand(ctx, "resume",
		fmt.Sprintf(`claude -p --resume %s --output-format json "Which word did I ask you to remember?" </dev/null | head -c 2000`, claudeSession), false, false)
	if err != nil {
		return err
	}
	fmt.Printf("\n--- resumed %s: exit %v\n%s\n", claudeSession, resumed["exitCode"], resumed["stdout"])
	return process.DeleteSession(ctx, "resume")
}

// userMessage is one line of claude's stream-json input.
func userMessage(text string) string {
	line, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": text}}},
	})
	return string(line) + "\n"
}

// event is the part of claude's stream-json output this example reads.
type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	Message   struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	err error
}

// awaitResult prints the turn's tool calls and returns at its result event,
// which marks the end of the answer however long it took.
func awaitResult(events <-chan event) (string, string, error) {
	for ev := range events {
		switch {
		case ev.err != nil:
			return "", "", ev.err
		case ev.Type == "assistant":
			for _, part := range ev.Message.Content {
				if part.Type == "tool_use" {
					fmt.Printf("  [%s] %s %s\n", time.Now().Format("15:04:05"), part.Name, abbreviate(string(part.Input)))
				}
			}
		case ev.Type == "result":
			return ev.Result, ev.SessionID, nil
		}
	}
	return "", "", errors.New("claude ended before answering")
}

// follow turns the command's stdout into events. The stream gateway may
// drop a long-lived connection; the logs stream replays from the start, so
// a reconnect skips what was already read.
func follow(ctx context.Context, process *mogenius.ProcessService, sessionID, cmdID string) <-chan event {
	events := make(chan event)
	go func() {
		defer close(events)
		seen, partial := 0, ""
		for {
			stdout, stderr := make(chan string), make(chan string)
			done := make(chan error, 1)
			go func() { done <- process.GetSessionCommandLogsStream(ctx, sessionID, cmdID, stdout, stderr) }()
			go func() {
				for range stderr {
				}
			}()
			offset := 0
			for chunk := range stdout {
				if skip := min(max(seen-offset, 0), len(chunk)); skip > 0 {
					offset += skip
					chunk = chunk[skip:]
				}
				offset += len(chunk)
				seen = max(seen, offset)
				lines := strings.Split(partial+chunk, "\n")
				partial = lines[len(lines)-1]
				for _, line := range lines[:len(lines)-1] {
					var ev event
					if json.Unmarshal([]byte(line), &ev) == nil {
						events <- ev
					}
				}
			}
			err := <-done
			info, infoErr := process.GetSessionCommand(ctx, sessionID, cmdID)
			if infoErr == nil && info["exitCode"] != nil {
				return
			}
			var gone *sdkerrors.MogeniusNotFoundError
			if errors.As(infoErr, &gone) {
				events <- event{err: fmt.Errorf("the session is gone: %w", infoErr)}
				return
			}
			if err != nil {
				fmt.Printf("  (log stream dropped: %v — reconnecting)\n", err)
			}
			select {
			case <-ctx.Done():
				events <- event{err: ctx.Err()}
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return events
}

func abbreviate(text string) string {
	scanner := bufio.NewScanner(strings.NewReader(text))
	if scanner.Scan() {
		text = scanner.Text()
	}
	if len(text) > 100 {
		return text[:100] + "…"
	}
	return text
}
