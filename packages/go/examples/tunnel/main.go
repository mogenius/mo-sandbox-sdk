// Command tunnel starts an HTTP server in a sandbox and reaches it through a
// tunnel — once through the local port, once in-process with DialContext.
//
// Run from packages/go: set -a; . ../../.env; set +a; go run ./examples/tunnel
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
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
		SandboxBaseParams: types.SandboxBaseParams{Labels: map[string]string{"example": "tunnel"}},
	}, options.WithTimeout(2*time.Minute))
	if err != nil {
		return err
	}
	defer func() {
		if deleteErr := sandbox.Delete(ctx); deleteErr != nil && err == nil {
			err = deleteErr
		}
		fmt.Println("deleted")
	}()
	if sandbox.ServiceFQDN != nil {
		fmt.Println("inside the cluster the sandbox is", *sandbox.ServiceFQDN)
	}

	// a session keeps the server running after the call returns
	if err := sandbox.Process.CreateSession(ctx, "web"); err != nil {
		return err
	}
	if _, err := sandbox.Process.ExecuteSessionCommand(ctx, "web",
		"cd /tmp && echo tunnel-ok > index.html && python3 -m http.server 8000", true, false); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	tunnel, err := sandbox.Tunnel(ctx, 8000)
	if err != nil {
		return err
	}
	defer tunnel.Close()

	fmt.Println("through", tunnel.URL()+":", get(http.DefaultClient, tunnel.URL()+"/index.html"))
	inProcess := &http.Client{Transport: &http.Transport{DialContext: tunnel.DialContext}}
	fmt.Println("through DialContext:", get(inProcess, "http://sandbox/index.html"))
	return nil
}

func get(client *http.Client, url string) string {
	response, err := client.Get(url)
	if err != nil {
		return err.Error()
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return fmt.Sprintf("%d %s", response.StatusCode, strings.TrimSpace(string(body)))
}
