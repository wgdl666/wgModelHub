package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/wgdl666/kangaroo/logs"
)

// Server 仅提供内网 /healthz 与 /metrics，不承载业务 RPC 或调试 API。
type Server struct {
	metricsHandler http.Handler
	server         *http.Server
}

func New(metricsHandler http.Handler) *Server {
	return &Server{metricsHandler: metricsHandler}
}

// Start 同步 net.Listen：bind 失败直接返回，避免 goroutine ListenAndServe 静默丢监控端口。
// addr 由启动边界已校验的 HTTPListenAddress 传入（例如 :51053）。
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealth)
	mux.Handle("/metrics", s.metricsHandler)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			logs.Default().Error("http_server_failed", "error", err)
		}
	}()
	logs.Default().Info("http_server_started", "addr", addr)
	return nil
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Stop 纳入进程优雅关闭，给 Prometheus scrape 与在途请求收尾窗口。
// 调用方仅在 Start 成功后 defer Stop；s.server 此时已成立。
func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
