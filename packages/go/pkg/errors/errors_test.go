package errors

import (
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

func kind(err error) string {
	switch err.(type) {
	case *MogeniusProcessExecutionTimeoutError:
		return "process-timeout"
	case *MogeniusTimeoutError:
		return "timeout"
	case *MogeniusOperatorUpgradeRequiredError:
		return "upgrade"
	case *MogeniusNotFoundError:
		return "not-found"
	case *MogeniusConflictError:
		return "conflict"
	case *MogeniusForbiddenError:
		return "forbidden"
	case *MogeniusValidationError:
		return "validation"
	case *MogeniusAuthenticationError:
		return "authentication"
	case *MogeniusRateLimitError:
		return "rate-limit"
	case *MogeniusServerError:
		return "server"
	case *MogeniusError:
		return "base"
	}
	return fmt.Sprintf("%T", err)
}

func TestKindFollowsErrorCodeThenStatus(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   string
	}{
		{408, "EXEC_TIMEOUT", "process-timeout"},
		{504, "OPERATOR_TIMEOUT", "timeout"},
		{424, "OPERATOR_UPGRADE_REQUIRED", "upgrade"},
		{404, "SANDBOX_NOT_FOUND", "not-found"},
		{404, "SESSION_COMMAND_NOT_FOUND", "not-found"},
		{409, "SESSION_BUSY", "conflict"},
		{409, "FILE_NOT_EMPTY", "conflict"},
		{403, "FILE_PERMISSION_DENIED", "forbidden"},
		{400, "SANDBOX_ENV_NOT_ALLOWED", "validation"},
		// the error code wins over the status
		{500, "SESSION_NOT_FOUND", "not-found"},
		{400, "", "validation"},
		{401, "", "authentication"},
		{403, "", "forbidden"},
		{404, "", "not-found"},
		{408, "", "process-timeout"},
		{409, "", "conflict"},
		{424, "", "upgrade"},
		{429, "", "rate-limit"},
		{502, "", "server"},
		{504, "", "timeout"},
		{418, "", "base"},
	}
	for _, c := range cases {
		body := fmt.Sprintf(`{"statusCode":%d,"errorCode":%q,"message":"m"}`, c.status, c.code)
		if got := kind(NewMogeniusErrorFromBody([]byte(body), c.status, nil)); got != c.want {
			t.Errorf("%d %q: got %s, want %s", c.status, c.code, got, c.want)
		}
	}
}

func TestFieldsComeFromTheBody(t *testing.T) {
	body := `{"statusCode":400,"errorCode":"INVALID_REQUEST","source":"operator","message":["name must be set","env is bad"]}`
	err := NewMogeniusErrorFromBody([]byte(body), 400, nil)

	var validation *MogeniusValidationError
	if !stderrors.As(err, &validation) {
		t.Fatalf("got %T", err)
	}
	if validation.Message != "name must be set, env is bad" || validation.ErrorCode != "INVALID_REQUEST" ||
		validation.Source != SourceOperator || validation.StatusCode != 400 {
		t.Fatalf("unexpected fields: %+v", validation.MogeniusError)
	}
	if err.Error() != "Validation error: name must be set, env is bad" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestMessageFallbacks(t *testing.T) {
	cases := map[string]string{
		"upstream down":             "upstream down",
		`{"error":"Bad Gateway"}`:   "Bad Gateway",
		"":                          "GET /x → HTTP 502",
		`{"statusCode":502}`:        "GET /x → HTTP 502",
		`{"message":"","error":""}`: "GET /x → HTTP 502",
	}
	for body, want := range cases {
		var base *MogeniusError
		err := NewMogeniusErrorFromResponse([]byte(body), 502, nil, "GET /x → HTTP 502")
		if !stderrors.As(err, &base) || base.Message != want || base.Source != SourceAPI {
			t.Errorf("body %q: got %+v, want message %q from the api", body, base, want)
		}
	}
}

func TestEveryLevelIsReachable(t *testing.T) {
	err := fmt.Errorf("running: %w", NewMogeniusErrorFromBody([]byte(`{"errorCode":"EXEC_TIMEOUT","message":"stopped after 10 s"}`), 408, nil))

	var process *MogeniusProcessExecutionTimeoutError
	var timeout *MogeniusTimeoutError
	var base *MogeniusError
	if !stderrors.As(err, &process) || !stderrors.As(err, &timeout) || !stderrors.As(err, &base) {
		t.Fatalf("errors.As did not reach every level of %v", err)
	}
	if base.ErrorCode != "EXEC_TIMEOUT" || base.StatusCode != 408 {
		t.Fatalf("unexpected base %+v", base)
	}
}

func TestConstructorsSetTheStatus(t *testing.T) {
	cases := []struct {
		err    error
		status int
		prefix string
	}{
		{NewMogeniusError("m", 0, nil), 0, "mogenius error: m"},
		{NewMogeniusError("m", 500, nil), 500, "mogenius error (status 500): m"},
		{NewMogeniusAuthenticationError("m", nil), 401, "Authentication failed: m"},
		{NewMogeniusForbiddenError("m", nil), 403, "Forbidden: m"},
		{NewMogeniusNotFoundError("m", nil), 404, "Resource not found: m"},
		{NewMogeniusConflictError("m", nil), 409, "Conflict: m"},
		{NewMogeniusValidationError("m", nil), 400, "Validation error: m"},
		{NewMogeniusRateLimitError("m", nil), 429, "Rate limit exceeded: m"},
		{NewMogeniusServerError("m", 503, nil), 503, "Server error: m"},
		{NewMogeniusTimeoutError("m"), 0, "Operation timed out: m"},
		{NewMogeniusProcessExecutionTimeoutError("m", nil), 408, "Command timed out: m"},
		{NewMogeniusOperatorUpgradeRequiredError("m", nil), 424, "Operator upgrade required: m"},
	}
	for _, c := range cases {
		var base *MogeniusError
		if !stderrors.As(c.err, &base) || base.StatusCode != c.status || base.Source != SourceSDK || c.err.Error() != c.prefix {
			t.Errorf("%T: status %d, Error() %q", c.err, base.StatusCode, c.err.Error())
		}
	}
}

func TestUnsupportedNamesTheAlternative(t *testing.T) {
	err := NewMogeniusUnsupportedError("Forking a running sandbox", "Create a new sandbox from the same profile.")
	if !strings.HasPrefix(err.Error(), "Forking a running sandbox is not supported") ||
		!strings.HasSuffix(err.Error(), "Create a new sandbox from the same profile.") {
		t.Fatalf("Error() = %q", err.Error())
	}
	if err.ErrorCode != "UNSUPPORTED" || err.Source != SourceSDK || err.Feature != "Forking a running sandbox" {
		t.Fatalf("unexpected fields %+v", err)
	}
}
