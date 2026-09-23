package callledger

import (
	"context"
	"errors"
	"strings"

	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// CallerFromContext 复用视频幂等命名空间：公网主体覆盖自报 caller，缺失记 unknown。
func CallerFromContext(ctx context.Context) string {
	if publicCaller, ok := auth.PublicCaller(ctx); ok {
		return publicCaller
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return UnknownLabel
	}
	values := md.Get(protocol.CallerMetadataKey)
	if len(values) == 0 {
		return UnknownLabel
	}
	if v := strings.TrimSpace(values[0]); v != "" {
		return v
	}
	return UnknownLabel
}

// BusinessSceneFromContext 读取可选业务场景 metadata；未打标签为 unknown，不猜。
func BusinessSceneFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return UnknownLabel
	}
	values := md.Get(protocol.BusinessSceneMetadataKey)
	if len(values) == 0 {
		return UnknownLabel
	}
	if v := strings.TrimSpace(values[0]); v != "" {
		return v
	}
	return UnknownLabel
}

// ClassifyError 仅在有证据时归因；HTTP 400 不得单独判定为 caller_request。
func ClassifyError(err error) (category, code, reason, message string) {
	if err == nil {
		return "", "", "", ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCategoryCancelled, codes.Canceled.String(), "CONTEXT_CANCELED", sanitizeErrorMessage(err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorCategoryCancelled, codes.DeadlineExceeded.String(), "CONTEXT_DEADLINE", sanitizeErrorMessage(err.Error())
	}

	st, ok := status.FromError(err)
	if !ok {
		st = status.Convert(provider.ToStatus(err))
	}
	code = st.Code().String()
	message = sanitizeErrorMessage(st.Message())
	kind := provider.Kind(err)

	switch {
	case st.Code() == codes.Canceled:
		return ErrorCategoryCancelled, code, string(kind), message
	case kind == provider.ErrorConfiguration:
		// 凭据/路由配置失败归属 ModelHub，不把配错密钥归咎调用业务。
		return ErrorCategoryModelHub, code, string(kind), message
	case kind == provider.ErrorInvalidArgument && provider.IsNotAttempted(err):
		return ErrorCategoryCallerRequest, code, string(kind), message
	case kind == provider.ErrorInvalidArgument:
		// 供应商 HTTP 400 已触达上游：证据不足时标 unknown，禁止仅凭 400 归因 caller。
		return ErrorCategoryUnknown, code, string(kind), message
	case kind == provider.ErrorRateLimited || kind == provider.ErrorContentBlocked ||
		kind == provider.ErrorUnavailable || kind == provider.ErrorTimeout ||
		kind == provider.ErrorInvalidResponse || kind == provider.ErrorSubmitOutcomeUnknown:
		return ErrorCategoryProvider, code, string(kind), message
	default:
		return ErrorCategoryUnknown, code, string(kind), message
	}
}
