package publicrpc

import (
	"net/http"
	"strings"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

// 浏览器 grpc-web 只放行运营台来源，避免任意站带着公网 Key 扫清单。
var allowedOrigins = map[string]struct{}{
	"https://ops.wgdl.tech":      {},
	"http://127.0.0.1:5173":      {},
	"http://localhost:5173":      {},
	"http://127.0.0.1:4173":      {},
	"http://localhost:4173":      {},
}

// AllowOrigin 给 grpc-web CORS 用；原生 gRPC 客户端不走 Origin。
func AllowOrigin(origin string) bool {
	_, ok := allowedOrigins[strings.TrimSpace(origin)]
	return ok
}

// Handler 在同一公网口上同时接原生 gRPC 与浏览器 grpc-web。
func Handler(grpcServer *grpc.Server) http.Handler {
	wrapped := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(AllowOrigin),
		grpcweb.WithAllowedRequestHeaders([]string{"*"}),
	)
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if wrapped.IsGrpcWebRequest(request) || wrapped.IsAcceptableGrpcCorsRequest(request) {
			wrapped.ServeHTTP(writer, request)
			return
		}
		grpcServer.ServeHTTP(writer, request)
	})
	// ACK 前置已做 TLS；Pod 内仍是明文，必须 h2c 才能让 grpcurl / Go client 继续走原生 gRPC。
	return h2c.NewHandler(inner, &http2.Server{})
}
