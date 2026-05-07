package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// LLMErrorKind classifies LLM backend errors.
type LLMErrorKind string

const (
	ErrRateLimit     LLMErrorKind = "rate_limit"
	ErrValidation    LLMErrorKind = "validation_error"
	ErrTimeout       LLMErrorKind = "timeout"
	ErrServerError   LLMErrorKind = "server_error"
	ErrEmptyResponse LLMErrorKind = "empty_response"
	ErrUnknown       LLMErrorKind = "unknown"
)

// LLMError wraps an LLM backend error with classification.
type LLMError struct {
	Kind       LLMErrorKind
	StatusCode int
	Message    string
	Err        error
}

func (e *LLMError) Error() string {
	return fmt.Sprintf("llm %s: %s", e.Kind, e.Err)
}

func (e *LLMError) Unwrap() error {
	return e.Err
}

// Retryable returns true if this error kind might succeed on retry.
func (e *LLMError) Retryable() bool {
	switch e.Kind {
	case ErrValidation:
		return false // deterministic failure
	default:
		return true
	}
}

// classifyLLMError wraps a raw error with an LLMErrorKind classification.
func classifyLLMError(err error, statusCode int) *LLMError {
	if err == nil {
		return nil
	}

	kind := ErrUnknown
	msg := err.Error()

	// Context errors take priority.
	if errors.Is(err, context.DeadlineExceeded) {
		kind = ErrTimeout
	} else {
		kind = classifyByStatusAndMessage(statusCode, msg)
	}

	return &LLMError{Kind: kind, StatusCode: statusCode, Message: msg, Err: err}
}

func classifyByStatusAndMessage(status int, msg string) LLMErrorKind {
	lower := strings.ToLower(msg)
	switch {
	case status == http.StatusTooManyRequests,
		strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "rate_limit"),
		strings.Contains(lower, "usage limit"):
		return ErrRateLimit
	case status == http.StatusBadRequest,
		strings.Contains(lower, "no tool call found"),
		strings.Contains(lower, "invalid_request_error"),
		strings.Contains(lower, "validation"):
		return ErrValidation
	case status >= 500:
		return ErrServerError
	case strings.Contains(lower, "no choices"),
		strings.Contains(lower, "empty"):
		return ErrEmptyResponse
	default:
		return ErrUnknown
	}
}
