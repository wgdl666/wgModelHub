package callmeta

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc/stats"
)

// GRPCClientHandler/GRPCServerHandler 同时建立 OTel Span 和传播业务关联，避免两套拦截器覆盖父 Span。
func GRPCClientHandler() stats.Handler {
	return otelgrpc.NewClientHandler(otelgrpc.WithPropagators(Propagator{}))
}
func GRPCServerHandler() stats.Handler {
	return otelgrpc.NewServerHandler(otelgrpc.WithPropagators(Propagator{}))
}
