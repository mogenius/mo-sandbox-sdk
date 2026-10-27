package mogenius

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	sdkerrors "github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/errors"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/options"
	"github.com/mogenius/mo-sandbox-sdk/packages/go/pkg/types"
)

const (
	DefaultAPIURL    = "https://platform-api.mogenius.com"
	DefaultStreamURL = "wss://k8s-cmd-stream.mogenius.com"
	DefaultNamespace = "agent-sandbox"
	DefaultProfile   = "default"
)

// resolvedConfig is everything a sandbox needs from the config, with the
// environment and the defaults filled in.
type resolvedConfig struct {
	apiKey         string
	apiURL         string
	streamURL      string
	organizationID string
	clusterID      string
	namespace      string
	profile        string
	workspaceName  string
	httpClient     *http.Client
	fileTransport  options.Transport
}

// resolveConfig lets explicit config win and environment variables fill the
// gaps. Only the key is required: an API key with a single organization and
// cluster scope needs no ids, the platform picks them from the key.
func resolveConfig(config *types.MogeniusConfig) (*resolvedConfig, error) {
	if config == nil {
		config = &types.MogeniusConfig{}
	}
	apiKey := first(config.APIKey, env("MOGENIUS_API_KEY"))
	if apiKey == "" {
		return nil, sdkerrors.NewMogeniusError(
			"No API key: pass APIKey to NewClientWithConfig or set MOGENIUS_API_KEY. Create one under Organization → API keys.",
			0, nil)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	transport := options.Transport(strings.ToLower(first(config.FileTransport, env("MOGENIUS_FILE_TRANSPORT"), string(options.TransportHTTP))))
	if transport != options.TransportHTTP && transport != options.TransportSFTP {
		return nil, sdkerrors.NewMogeniusValidationError(
			fmt.Sprintf("FileTransport (MOGENIUS_FILE_TRANSPORT) is %q; it is http or sftp.", transport), nil)
	}
	return &resolvedConfig{
		apiKey:         apiKey,
		apiURL:         strings.TrimRight(first(config.APIUrl, env("MOGENIUS_API_URL"), DefaultAPIURL), "/"),
		streamURL:      strings.TrimRight(first(config.StreamURL, env("MOGENIUS_STREAM_URL"), DefaultStreamURL), "/"),
		organizationID: first(config.OrganizationID, env("MOGENIUS_ORGANIZATION_ID")),
		clusterID:      first(config.ClusterID, env("MOGENIUS_CLUSTER_ID")),
		namespace:      first(config.Namespace, config.Target, env("MOGENIUS_SANDBOX_NAMESPACE"), DefaultNamespace),
		profile:        first(config.Profile, env("MOGENIUS_SANDBOX_PROFILE"), DefaultProfile),
		workspaceName:  first(config.WorkspaceName, env("MOGENIUS_WORKSPACE_NAME")),
		httpClient:     httpClient,
		fileTransport:  transport,
	}, nil
}

func env(name string) string {
	return os.Getenv(name)
}

// first is the first value that is not blank, trimmed.
func first(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
