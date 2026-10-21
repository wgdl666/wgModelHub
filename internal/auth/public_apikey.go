package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/wgdl666/wgModelHub/internal/apikeystore"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type publicAuthKey struct{}

// PublicCaller 返回公网鉴权后的稳定 caller（public:<principal_id>）；内网未鉴权链路不存在该值。
func PublicCaller(ctx context.Context) (string, bool) {
	auth, ok := ctx.Value(publicAuthKey{}).(apikeystore.Principal)
	if !ok || strings.TrimSpace(auth.PrincipalID) == "" {
		return "", false
	}
	return "public:" + auth.PrincipalID, true
}

// UnaryServerInterceptor 在公网 gRPC Server 上强制 API Key；health 检查放行。
// 仅 ErrInvalid 映射 Unauthenticated；store 内部故障映射 Unavailable，避免误导调用方误判 Key。
func UnaryServerInterceptor(keys *apikeystore.Store) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isHealthMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, err := authenticate(ctx, keys)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor 与 Unary 相同策略，覆盖 ModelHub 全部 server-streaming RPC。
func StreamServerInterceptor(keys *apikeystore.Store) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if isHealthMethod(info.FullMethod) {
			return handler(srv, ss)
		}
		ctx, err := authenticate(ss.Context(), keys)
		if err != nil {
			return err
		}
		return handler(srv, &authenticatedServerStream{ServerStream: ss, ctx: ctx})
	}
}

type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedServerStream) Context() context.Context {
	return s.ctx
}

// 公网 RPC 的业务 Span 在收包后才开始，单独计时才能区分大包上传和服务端执行。
func (s *authenticatedServerStream) RecvMsg(message any) error {
	ctx, span := telemetry.StartSpan(s.ctx, "modelhub.public_receive")
	defer span.End()
	err := s.ServerStream.RecvMsg(message)
	if err != nil && !errors.Is(err, io.EOF) {
		telemetry.RecordError(ctx, err)
	}
	return err
}

func authenticate(ctx context.Context, keys *apikeystore.Store) (context.Context, error) {
	if keys == nil {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}
	// 不采集 token 或主体资料，只观测公网鉴权查库实际用了多久。
	authCtx, authSpan := telemetry.StartSpan(ctx, "modelhub.public_auth")
	principal, err := keys.Authenticate(authCtx, values[0])
	authSpan.End()
	if err != nil {
		if errors.Is(err, apikeystore.ErrInvalid) {
			// 不区分缺失/过期/吊销/hash 不匹配，避免向公网泄露 Key 状态。
			return ctx, status.Error(codes.Unauthenticated, "authentication required")
		}
		// 不得记录 authorization 或 secret；基础设施故障不应伪装成坏 Key。
		slog.Error("public api key store error", "err", err)
		return ctx, status.Error(codes.Unavailable, "authentication temporarily unavailable")
	}
	return context.WithValue(ctx, publicAuthKey{}, principal), nil
}

// ContextWithPublicPrincipal 仅供单测或集成测试构造公网鉴权 context；生产路径必须走 gRPC 拦截器。
func ContextWithPublicPrincipal(ctx context.Context, principalID, keyID string) context.Context {
	return context.WithValue(ctx, publicAuthKey{}, apikeystore.Principal{
		PrincipalID: strings.TrimSpace(principalID),
		KeyID:       strings.TrimSpace(keyID),
	})
}

func isHealthMethod(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/")
}
