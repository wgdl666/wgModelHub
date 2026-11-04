package httpserver_test

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/internal/infra/httpserver"
)

func TestMonitoringEndpoints(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok_metrics\n")
	})
	srv := httpserver.New(metrics)
	if err := srv.Start(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	base := fmt.Sprintf("http://%s", addr)
	healthResp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", healthResp.StatusCode)
	}

	metricsResp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer metricsResp.Body.Close()
	body, _ := io.ReadAll(metricsResp.Body)
	if string(body) != "ok_metrics\n" {
		t.Fatalf("metrics body=%q", body)
	}

	heapResp, err := http.Get(base + "/debug/pprof/heap")
	if err != nil {
		t.Fatal(err)
	}
	defer heapResp.Body.Close()
	if heapResp.StatusCode != http.StatusOK {
		t.Fatalf("heap status=%d", heapResp.StatusCode)
	}
	profile, err := gzip.NewReader(heapResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer profile.Close()
	if size, err := io.Copy(io.Discard, profile); err != nil || size == 0 {
		t.Fatalf("heap profile size=%d, err=%v", size, err)
	}
}

func TestStartBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	metrics := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	srv := httpserver.New(metrics)
	// 端口已被占用时 Start 必须同步失败，不能只写日志。
	if err := srv.Start(ln.Addr().String()); err == nil {
		t.Fatal("expected bind failure")
	}
}
