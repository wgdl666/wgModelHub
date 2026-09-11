package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ErrorKind string

const (
	ErrorInvalidArgument ErrorKind = "INVALID_ARGUMENT"
	ErrorConfiguration   ErrorKind = "CONFIGURATION_ERROR"
	ErrorRateLimited     ErrorKind = "RATE_LIMITED"
	ErrorContentBlocked  ErrorKind = "CONTENT_BLOCKED"
	ErrorUnavailable     ErrorKind = "PROVIDER_UNAVAILABLE"
	ErrorTimeout         ErrorKind = "TIMEOUT"
	ErrorInvalidResponse ErrorKind = "INVALID_RESPONSE"
	// ErrorSubmitOutcomeUnknown：Submit 受理结果不确定（timeout/cancel/unavailable）时落库 FAILED 的稳定 reason；禁止自动重提。
	ErrorSubmitOutcomeUnknown ErrorKind = "SUBMIT_OUTCOME_UNKNOWN"
)

// Error 把供应商差异收敛为稳定分类；Message 不携带 Prompt、媒体正文或密钥。
type Error struct {
	Kind    ErrorKind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

func Wrap(kind ErrorKind, message string, err error) error {
	return &Error{Kind: kind, Message: message, Err: err}
}

func New(kind ErrorKind, message string) error {
	return &Error{Kind: kind, Message: message}
}

func Errorf(kind ErrorKind, format string, args ...any) error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

func FromHTTP(providerName string, statusCode int) error {
	return FromHTTPDetail(providerName, statusCode, "")
}

// httpErrorDetailLimit 只保留供应商拒因摘要；完整 Prompt/密钥不得进入 Message。
const httpErrorDetailLimit = 512

// FromHTTPDetail 把供应商 HTTP 错误正文截断后附在分类消息上，供 gRPC status / Hub turn_error 对照。
func FromHTTPDetail(providerName string, statusCode int, detail string) error {
	message := fmt.Sprintf("%s returned HTTP %d", providerName, statusCode)
	if snippet := CompactHTTPErrorDetail(detail); snippet != "" {
		message += ": " + snippet
	}
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return New(ErrorInvalidArgument, message)
	case http.StatusUnauthorized, http.StatusForbidden:
		return New(ErrorConfiguration, message)
	case http.StatusTooManyRequests:
		return New(ErrorRateLimited, message)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return New(ErrorTimeout, message)
	default:
		if statusCode >= http.StatusInternalServerError {
			return New(ErrorUnavailable, message)
		}
		return New(ErrorUnavailable, message)
	}
}

// CompactHTTPErrorDetail 压空白并截断供应商错误正文，避免把超长 HTML/堆栈送进 status。
func CompactHTTPErrorDetail(detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return ""
	}
	if utf8.RuneCountInString(detail) <= httpErrorDetailLimit {
		return detail
	}
	runes := []rune(detail)
	return string(runes[:httpErrorDetailLimit]) + "…"
}

func Kind(err error) ErrorKind {
	var providerError *Error
	if errors.As(err, &providerError) {
		return providerError.Kind
	}
	return ErrorUnavailable
}
