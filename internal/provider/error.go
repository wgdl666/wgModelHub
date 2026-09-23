package provider

import (
	"errors"
	"fmt"
	"io"
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
	// Local=true 仅表示显式本地请求校验、尚未向供应商发请求；不得用 New/Errorf 推断。
	Local bool
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
	// 继承底层 Local：本地拒识被 Wrap 后仍不得伪装成已调供应商。
	local := false
	var providerError *Error
	if errors.As(err, &providerError) {
		local = providerError.Local
	}
	return &Error{Kind: kind, Message: message, Err: err, Local: local}
}

// WrapNotAttempted 标记出站前本地构造失败（create/marshal/build request 等），从未触达供应商。
func WrapNotAttempted(kind ErrorKind, message string, err error) error {
	return &Error{Kind: kind, Message: message, Err: err, Local: true}
}

// AsNotAttempted 在调用点把错误标为未触达供应商（如解析输入 data URI、拉取输入 URL）。
// 不得用于已拿到供应商 HTTP 响应的路径（FromHTTPDetail 必须保持 Local=false）。
func AsNotAttempted(err error) error {
	if err == nil {
		return nil
	}
	var providerError *Error
	if errors.As(err, &providerError) {
		if providerError.Local {
			return err
		}
		return &Error{Kind: providerError.Kind, Message: providerError.Message, Err: providerError.Err, Local: true}
	}
	return &Error{Kind: ErrorInvalidArgument, Message: err.Error(), Err: err, Local: true}
}

// New 构造普通供应商/协议错误；默认视为可能已触达上游，账本可落库。
func New(kind ErrorKind, message string) error {
	return &Error{Kind: kind, Message: message, Local: false}
}

// Errorf 同 New，带格式化。
func Errorf(kind ErrorKind, format string, args ...any) error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Local: false}
}

// NotAttempted 标记本地请求校验失败：从未向供应商发请求，账本不得记成真实调用。
func NotAttempted(kind ErrorKind, message string) error {
	return &Error{Kind: kind, Message: message, Local: true}
}

// NotAttemptedf 同 NotAttempted，带格式化。
func NotAttemptedf(kind ErrorKind, format string, args ...any) error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...), Local: true}
}

// IsNotAttempted 为 true 时账本不得落库：显式本地拒识，尚未产生真实调用。
func IsNotAttempted(err error) bool {
	var providerError *Error
	if errors.As(err, &providerError) {
		return providerError.Local
	}
	return false
}

func FromHTTP(providerName string, statusCode int) error {
	return FromHTTPDetail(providerName, statusCode, "")
}

// httpErrorDetailLimit 只保留供应商拒因摘要；完整 Prompt/密钥不得进入 Message。
const httpErrorDetailLimit = 512

// errorBodyReadLimit 只读拒因。成功响应里的图片、音频和视频不能从这里读。
const errorBodyReadLimit = 2048

// TakeHTTPError 读取非成功响应正文，截断后按状态码分类。各厂商字段不同，原文留给调用方对照。
func TakeHTTPError(providerName string, statusCode int, body io.Reader) error {
	detail := ""
	if body != nil {
		raw, _ := io.ReadAll(io.LimitReader(body, errorBodyReadLimit))
		detail = string(raw)
	}
	return FromHTTPDetail(providerName, statusCode, detail)
}

// FromHTTPDetail 把供应商 HTTP 错误正文截断后附在分类消息上，供 gRPC status / Hub turn_error 对照。
func FromHTTPDetail(providerName string, statusCode int, detail string) error {
	message := fmt.Sprintf("%s returned HTTP %d", providerName, statusCode)
	if snippet := CompactHTTPErrorDetail(detail); snippet != "" {
		message += ": " + snippet
	}
	var kind ErrorKind
	switch statusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = ErrorInvalidArgument
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = ErrorConfiguration
	case http.StatusTooManyRequests:
		kind = ErrorRateLimited
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		kind = ErrorTimeout
	default:
		kind = ErrorUnavailable
	}
	// HTTP 响应已证明触达供应商；Local 必须为 false，即使状态码是 400。
	return &Error{Kind: kind, Message: message, Local: false}
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
