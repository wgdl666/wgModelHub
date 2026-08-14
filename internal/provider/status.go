package provider

import (
	"context"
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const errorInfoDomain = "wg.modelhub"

// ToStatus 把供应商错误收敛为带 ErrorInfo.reason 的 gRPC status。
// Message 只保留分类与操作信息，禁止回填 Prompt、媒体正文或密钥。
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		st := status.New(codes.DeadlineExceeded, "request timed out")
		return withReason(st, ErrorTimeout)
	}
	var providerError *Error
	if !errors.As(err, &providerError) {
		st := status.New(codes.Unavailable, "provider unavailable")
		return withReason(st, ErrorUnavailable)
	}
	code, message := mapKind(providerError.Kind, providerError.Message)
	st := status.New(code, message)
	return withReason(st, providerError.Kind)
}

func mapKind(kind ErrorKind, message string) (codes.Code, string) {
	if message == "" {
		message = string(kind)
	}
	switch kind {
	case ErrorInvalidArgument:
		return codes.InvalidArgument, message
	case ErrorConfiguration:
		return codes.FailedPrecondition, message
	case ErrorRateLimited:
		return codes.ResourceExhausted, message
	case ErrorContentBlocked:
		return codes.FailedPrecondition, message
	case ErrorTimeout:
		return codes.DeadlineExceeded, message
	case ErrorInvalidResponse:
		return codes.Internal, message
	default:
		return codes.Unavailable, message
	}
}

func withReason(st *status.Status, kind ErrorKind) error {
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: string(kind),
		Domain: errorInfoDomain,
	})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}
