// Package errors holds the error types of the mogenius sandbox SDK.
//
// Every failed call returns a *MogeniusError or one of the kinds below, and
// every kind unwraps to its *MogeniusError: errors.As with a *MogeniusError
// target reaches the status code, the platform's error code and who decided,
// whatever the kind. A cancelled or expired context is returned as is, so
// errors.Is(err, context.DeadlineExceeded) keeps working.
//
// The package shadows the standard library's errors; import it under another
// name next to it, e.g. sdkerrors.
package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Who decided an error: the platform API, the cluster's operator or the SDK itself.
const (
	SourceAPI      = "api"
	SourceOperator = "operator"
	SourceSDK      = "sdk"
)

// MogeniusError is the base of every SDK error. It carries what the platform
// said, so a caller can branch on ErrorCode or StatusCode without parsing text.
type MogeniusError struct {
	Message    string
	StatusCode int
	Headers    http.Header
	// ErrorCode is the platform's machine-readable code, e.g. SANDBOX_NOT_FOUND; empty when it sent none.
	ErrorCode string
	// Source is who decided: SourceAPI, SourceOperator or SourceSDK.
	Source string
}

func (e *MogeniusError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("mogenius error (status %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("mogenius error: %s", e.Message)
}

// NewMogeniusError builds the base error, decided by the SDK unless Source is changed.
func NewMogeniusError(message string, statusCode int, headers http.Header) *MogeniusError {
	return &MogeniusError{Message: message, StatusCode: statusCode, Headers: headers, Source: SourceSDK}
}

// MogeniusAuthenticationError: the key was rejected (401).
type MogeniusAuthenticationError struct{ *MogeniusError }

func (e *MogeniusAuthenticationError) Error() string { return "Authentication failed: " + e.Message }
func (e *MogeniusAuthenticationError) Unwrap() error { return e.MogeniusError }

func NewMogeniusAuthenticationError(message string, headers http.Header) *MogeniusAuthenticationError {
	return &MogeniusAuthenticationError{NewMogeniusError(message, http.StatusUnauthorized, headers)}
}

// MogeniusForbiddenError: the key may not do this (403) — no EDITOR grant, no
// cluster role, RBAC in the cluster, or file permissions in the container.
type MogeniusForbiddenError struct{ *MogeniusError }

func (e *MogeniusForbiddenError) Error() string { return "Forbidden: " + e.Message }
func (e *MogeniusForbiddenError) Unwrap() error { return e.MogeniusError }

func NewMogeniusForbiddenError(message string, headers http.Header) *MogeniusForbiddenError {
	return &MogeniusForbiddenError{NewMogeniusError(message, http.StatusForbidden, headers)}
}

// MogeniusNotFoundError: no sandbox, container, profile, file, session or command of that name (404).
type MogeniusNotFoundError struct{ *MogeniusError }

func (e *MogeniusNotFoundError) Error() string { return "Resource not found: " + e.Message }
func (e *MogeniusNotFoundError) Unwrap() error { return e.MogeniusError }

func NewMogeniusNotFoundError(message string, headers http.Header) *MogeniusNotFoundError {
	return &MogeniusNotFoundError{NewMogeniusError(message, http.StatusNotFound, headers)}
}

// MogeniusConflictError: the sandbox or session is in the wrong state for
// this — not bound yet, suspended, name taken, a command still running (409).
type MogeniusConflictError struct{ *MogeniusError }

func (e *MogeniusConflictError) Error() string { return "Conflict: " + e.Message }
func (e *MogeniusConflictError) Unwrap() error { return e.MogeniusError }

func NewMogeniusConflictError(message string, headers http.Header) *MogeniusConflictError {
	return &MogeniusConflictError{NewMogeniusError(message, http.StatusConflict, headers)}
}

// MogeniusValidationError: the request itself is wrong (400) — a bad label, env name or parameter.
type MogeniusValidationError struct{ *MogeniusError }

func (e *MogeniusValidationError) Error() string { return "Validation error: " + e.Message }
func (e *MogeniusValidationError) Unwrap() error { return e.MogeniusError }

func NewMogeniusValidationError(message string, headers http.Header) *MogeniusValidationError {
	return &MogeniusValidationError{NewMogeniusError(message, http.StatusBadRequest, headers)}
}

// MogeniusRateLimitError: too many requests (429).
type MogeniusRateLimitError struct{ *MogeniusError }

func (e *MogeniusRateLimitError) Error() string { return "Rate limit exceeded: " + e.Message }
func (e *MogeniusRateLimitError) Unwrap() error { return e.MogeniusError }

func NewMogeniusRateLimitError(message string, headers http.Header) *MogeniusRateLimitError {
	return &MogeniusRateLimitError{NewMogeniusError(message, http.StatusTooManyRequests, headers)}
}

// MogeniusServerError: the platform failed (5xx).
type MogeniusServerError struct{ *MogeniusError }

func (e *MogeniusServerError) Error() string { return "Server error: " + e.Message }
func (e *MogeniusServerError) Unwrap() error { return e.MogeniusError }

func NewMogeniusServerError(message string, statusCode int, headers http.Header) *MogeniusServerError {
	return &MogeniusServerError{NewMogeniusError(message, statusCode, headers)}
}

// MogeniusTimeoutError: a wait ran out — 504 from the operator, or the SDK's own deadline.
type MogeniusTimeoutError struct{ *MogeniusError }

func (e *MogeniusTimeoutError) Error() string { return "Operation timed out: " + e.Message }
func (e *MogeniusTimeoutError) Unwrap() error { return e.MogeniusError }

func NewMogeniusTimeoutError(message string) *MogeniusTimeoutError {
	return &MogeniusTimeoutError{NewMogeniusError(message, 0, nil)}
}

// MogeniusProcessExecutionTimeoutError: the command ran longer than its
// timeout and was stopped (408). It is a MogeniusTimeoutError too.
type MogeniusProcessExecutionTimeoutError struct{ *MogeniusTimeoutError }

func (e *MogeniusProcessExecutionTimeoutError) Error() string {
	return "Command timed out: " + e.Message
}
func (e *MogeniusProcessExecutionTimeoutError) Unwrap() error { return e.MogeniusTimeoutError }

func NewMogeniusProcessExecutionTimeoutError(message string, headers http.Header) *MogeniusProcessExecutionTimeoutError {
	return &MogeniusProcessExecutionTimeoutError{
		&MogeniusTimeoutError{NewMogeniusError(message, http.StatusRequestTimeout, headers)},
	}
}

// MogeniusOperatorUpgradeRequiredError: the cluster's operator is too old for this call (424).
type MogeniusOperatorUpgradeRequiredError struct{ *MogeniusError }

func (e *MogeniusOperatorUpgradeRequiredError) Error() string {
	return "Operator upgrade required: " + e.Message
}
func (e *MogeniusOperatorUpgradeRequiredError) Unwrap() error { return e.MogeniusError }

func NewMogeniusOperatorUpgradeRequiredError(message string, headers http.Header) *MogeniusOperatorUpgradeRequiredError {
	return &MogeniusOperatorUpgradeRequiredError{NewMogeniusError(message, http.StatusFailedDependency, headers)}
}

// MogeniusUnsupportedError: not available for Kubernetes sandboxes — fork,
// pause, archive, snapshots of running state. The message names the alternative.
type MogeniusUnsupportedError struct {
	*MogeniusError
	// Feature is what was asked for.
	Feature string
}

func (e *MogeniusUnsupportedError) Error() string { return e.Message }
func (e *MogeniusUnsupportedError) Unwrap() error { return e.MogeniusError }

func NewMogeniusUnsupportedError(feature, alternative string) *MogeniusUnsupportedError {
	message := feature + " is not supported by mogenius sandboxes (they are Kubernetes pods, not micro-VMs)."
	if alternative != "" {
		message += " " + alternative
	}
	base := NewMogeniusError(message, 0, nil)
	base.ErrorCode = "UNSUPPORTED"
	return &MogeniusUnsupportedError{MogeniusError: base, Feature: feature}
}

// errorBody is the error shape every platform route answers with.
type errorBody struct {
	ErrorCode string          `json:"errorCode"`
	Source    string          `json:"source"`
	Message   json.RawMessage `json:"message"` // a string, or a list of validation messages
	Error     string          `json:"error"`
}

// NewMogeniusErrorFromBody builds the right error kind from a failed platform response.
func NewMogeniusErrorFromBody(body []byte, statusCode int, headers http.Header) error {
	return NewMogeniusErrorFromResponse(body, statusCode, headers, fmt.Sprintf("HTTP %d", statusCode))
}

// NewMogeniusErrorFromResponse is NewMogeniusErrorFromBody with the message to
// use when the body has none. The platform's error code decides the kind
// first, the status second.
func NewMogeniusErrorFromResponse(body []byte, statusCode int, headers http.Header, fallback string) error {
	base := &MogeniusError{StatusCode: statusCode, Headers: headers, Source: SourceAPI}
	var parsed errorBody
	if json.Unmarshal(body, &parsed) == nil {
		base.Message = parseMessage(parsed.Message)
		if base.Message == "" {
			base.Message = parsed.Error
		}
		base.ErrorCode = parsed.ErrorCode
		if parsed.Source != "" {
			base.Source = parsed.Source
		}
	} else if text := strings.TrimSpace(string(body)); text != "" {
		base.Message = text
	}
	if base.Message == "" {
		base.Message = fallback
	}
	return classify(base)
}

func parseMessage(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, ", ")
	}
	return ""
}

func classify(base *MogeniusError) error {
	switch base.ErrorCode {
	case "EXEC_TIMEOUT":
		return &MogeniusProcessExecutionTimeoutError{&MogeniusTimeoutError{base}}
	case "OPERATOR_TIMEOUT":
		return &MogeniusTimeoutError{base}
	case "OPERATOR_UPGRADE_REQUIRED":
		return &MogeniusOperatorUpgradeRequiredError{base}
	case "SANDBOX_NOT_FOUND", "CONTAINER_NOT_FOUND", "SANDBOX_PROFILE_NOT_FOUND", "FILE_NOT_FOUND",
		"SESSION_NOT_FOUND", "SESSION_COMMAND_NOT_FOUND":
		return &MogeniusNotFoundError{base}
	case "SANDBOX_NOT_READY", "SANDBOX_ALREADY_EXISTS", "FILE_EXISTS", "FILE_NOT_EMPTY",
		"SESSION_ALREADY_EXISTS", "SESSION_BUSY":
		return &MogeniusConflictError{base}
	case "FILE_PERMISSION_DENIED":
		return &MogeniusForbiddenError{base}
	case "INVALID_REQUEST", "SANDBOX_ENV_NOT_ALLOWED":
		return &MogeniusValidationError{base}
	}
	switch status := base.StatusCode; {
	case status == http.StatusBadRequest:
		return &MogeniusValidationError{base}
	case status == http.StatusUnauthorized:
		return &MogeniusAuthenticationError{base}
	case status == http.StatusForbidden:
		return &MogeniusForbiddenError{base}
	case status == http.StatusNotFound:
		return &MogeniusNotFoundError{base}
	case status == http.StatusRequestTimeout:
		return &MogeniusProcessExecutionTimeoutError{&MogeniusTimeoutError{base}}
	case status == http.StatusConflict:
		return &MogeniusConflictError{base}
	case status == http.StatusFailedDependency:
		return &MogeniusOperatorUpgradeRequiredError{base}
	case status == http.StatusTooManyRequests:
		return &MogeniusRateLimitError{base}
	case status == http.StatusGatewayTimeout:
		return &MogeniusTimeoutError{base}
	case status >= 500:
		return &MogeniusServerError{base}
	default:
		return base
	}
}
