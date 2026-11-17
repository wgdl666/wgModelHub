package vmindgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"

	oss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/wgdl666/kangaroo/bizerr"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

// resultStorageError 保留原始 cause 供 Is/As 检查，阻止 SDK URL、正文及凭据进入错误文本。
type resultStorageError struct{ cause *bizerr.Error }

func (e *resultStorageError) Error() string { return e.cause.Message() }
func (e *resultStorageError) Unwrap() error { return e.cause }

var storageDiagnosticToken = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// SDK 的标识字段只接受有限字符；不输出 Message、Snapshot、Headers、对象 key 或 URL。
func diagnosticToken(value string) string {
	if storageDiagnosticToken.MatchString(value) {
		return value
	}
	return "redacted"
}

func resultOpenError(ctx context.Context, id string, err error) error {
	const message = "cannot open VMind result object"
	requestID := "invalid"
	if parsed, parseErr := uuid.Parse(id); parseErr == nil {
		requestID = parsed.String()
	}
	attrs := []any{"request_id", requestID, "trace_id", telemetry.TraceID(ctx), "phase", "open_reference", "error_type", fmt.Sprintf("%T", err)}
	level := slog.LevelWarn
	switch {
	case errors.Is(err, context.Canceled):
		level = slog.LevelDebug // 主动取消不新增告警，但错误链仍保留取消语义。
		attrs = append(attrs, "error_class", "cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		attrs = append(attrs, "error_class", "deadline_exceeded")
	default:
		var networkError net.Error
		if errors.As(err, &networkError) {
			attrs = append(attrs, "network_timeout", networkError.Timeout())
		}
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		attrs = append(attrs, "storage_code", diagnosticToken(apiError.ErrorCode()))
	}
	var responseError *awshttp.ResponseError
	if errors.As(err, &responseError) {
		attrs = append(attrs, "http_status", responseError.HTTPStatusCode(), "storage_request_id", diagnosticToken(responseError.ServiceRequestID()))
	}
	var ossError *oss.ServiceError
	if errors.As(err, &ossError) {
		attrs = append(attrs, "storage_code", diagnosticToken(ossError.Code), "http_status", ossError.StatusCode, "storage_request_id", diagnosticToken(ossError.RequestID))
	}
	slog.Log(ctx, level, "vmind_result_open_failed", attrs...)
	return provider.Wrap(provider.ErrorUnavailable, message, &resultStorageError{cause: bizerr.Wrap(provider.CodeResultObjectRead, err, message)})
}
