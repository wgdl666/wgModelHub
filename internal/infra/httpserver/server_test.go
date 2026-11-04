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

func TestHealthzAndMetrics(t *testing.T) {
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

func TestHeapProfile(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flag   string
		status int
	}{
		{name: "disabled", status: http.StatusNotFound},
		{name: "enabled", flag: "1", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WG_HEAP_PROFILE_ENABLED", tc.flag)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := ln.Addr().String()
			_ = ln.Close()

			srv := httpserver.New(http.NotFoundHandler())
			if err := srv.Start(addr); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Stop(context.Background()) })

			resp, err := http.Get("http://" + addr + "/debug/pprof/heap")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("heap status=%d, want %d", resp.StatusCode, tc.status)
			}
			if tc.status == http.StatusOK {
				profile, err := gzip.NewReader(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				defer profile.Close()
				if size, err := io.Copy(io.Discard, profile); err != nil || size == 0 {
					t.Fatalf("heap profile size=%d, err=%v", size, err)
				}
			}
		})
	}
}
