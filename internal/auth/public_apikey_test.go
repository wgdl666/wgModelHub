package auth

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPublicCallerFromContext(t *testing.T) {
	ctx := ContextWithPublicPrincipal(context.Background(), "pid-1", "kid-1")
	caller, ok := PublicCaller(ctx)
	if !ok || caller != "public:pid-1" {
		t.Fatalf("unexpected caller: ok=%v caller=%q", ok, caller)
	}
}

func TestUnaryInterceptorRejectsMissingAuth(t *testing.T) {
	interceptor := UnaryServerInterceptor(nil)
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestStreamInterceptorRejectsMissingAuth(t *testing.T) {
	interceptor := StreamServerInterceptor(nil)
	err := interceptor(nil, &fakeServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(any, grpc.ServerStream) error {
		return nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestUnaryInterceptorAllowsHealth(t *testing.T) {
	interceptor := UnaryServerInterceptor(nil)
	got, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	if err != nil || got != "ok" {
		t.Fatalf("health should pass without auth: got=%v err=%v", got, err)
	}
}

func TestUnaryInterceptorSetsPublicCaller(t *testing.T) {
	store, mat := openAuthTestStore(t)
	interceptor := UnaryServerInterceptor(store)
	md := metadata.Pairs("authorization", mat.Bearer)
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	var captured string
	_, err := interceptor(inCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(ctx context.Context, req any) (any, error) {
		captured, _ = PublicCaller(ctx)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if captured != "public:"+mat.PrincipalID {
		t.Fatalf("caller=%q want public:%s", captured, mat.PrincipalID)
	}
}

func TestStreamInterceptorSetsPublicCaller(t *testing.T) {
	store, mat := openAuthTestStore(t)
	interceptor := StreamServerInterceptor(store)
	md := metadata.Pairs("authorization", mat.Bearer)
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	var captured string
	err := interceptor(nil, &fakeServerStream{ctx: inCtx}, &grpc.StreamServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(_ any, stream grpc.ServerStream) error {
		captured, _ = PublicCaller(stream.Context())
		return nil
	})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if captured != "public:"+mat.PrincipalID {
		t.Fatalf("caller=%q want public:%s", captured, mat.PrincipalID)
	}
}

func TestAuthenticateInvalidKeyReturnsUnauthenticated(t *testing.T) {
	store, mat := openAuthTestStore(t)
	md := metadata.Pairs("authorization", "Bearer wgmh_"+mat.KeyID+"_wrong-secret")
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	_, err := authenticate(inCtx, store)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestAuthenticateStoreInternalErrorReturnsUnavailable(t *testing.T) {
	store, mat, db := openAuthTestStoreEx(t)
	// 关闭底层连接模拟 store 查询失败，不得映射为 Unauthenticated。
	if err := db.Close(); err != nil {
		t.Fatalf("close test db: %v", err)
	}
	md := metadata.Pairs("authorization", mat.Bearer)
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	_, err := authenticate(inCtx, store)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

func TestUnaryInterceptorInvalidKeyUnauthenticated(t *testing.T) {
	store, mat := openAuthTestStore(t)
	interceptor := UnaryServerInterceptor(store)
	md := metadata.Pairs("authorization", "Bearer wgmh_"+mat.KeyID+"_bad")
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	_, err := interceptor(inCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(ctx context.Context, req any) (any, error) {
		return nil, nil
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestUnaryInterceptorStoreErrorUnavailable(t *testing.T) {
	store, mat, db := openAuthTestStoreEx(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close test db: %v", err)
	}
	interceptor := UnaryServerInterceptor(store)
	md := metadata.Pairs("authorization", mat.Bearer)
	inCtx := metadata.NewIncomingContext(context.Background(), md)
	_, err := interceptor(inCtx, nil, &grpc.UnaryServerInfo{FullMethod: "/wg.modelhub.v2.ModelHubService/Generate"}, func(ctx context.Context, req any) (any, error) {
		return nil, nil
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context {
	return s.ctx
}
