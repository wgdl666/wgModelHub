package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	"github.com/wgdl666/wgModelHub/internal/infra/factory"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/protocol"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	telemetryRuntime, err := telemetry.Setup(ctx)
	if err != nil {
		fatal("telemetry_setup_failed", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = telemetryRuntime.Shutdown(shutdownCtx)
	}()

	bootstrap, err := config.LoadBootstrapFile(config.BootstrapFilePath)
	if err != nil {
		fatal("nacos_bootstrap_load_failed", err)
	}
	configLoader, err := config.NewNacosConfigLoader(bootstrap)
	if err != nil {
		fatal("nacos_loader_init_failed", err)
	}
	defer configLoader.Close()

	runtimeConfig, err := configLoader.Load(ctx)
	if err != nil {
		fatal("runtime_config_load_failed", err)
	}
	if runtimeConfig.Server.ListenAddress == "" {
		runtimeConfig.Server.ListenAddress = ":50053"
	}

	providers, err := factory.Build(ctx, runtimeConfig)
	if err != nil {
		fatal("provider_factory_failed", err)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(protocol.MaxRPCMessageBytes),
		grpc.MaxSendMsgSize(protocol.MaxRPCMessageBytes),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)
	modelhubv1.RegisterModelHubServiceServer(grpcServer, modelhub.New(runtimeConfig, providers))

	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	listener, err := net.Listen("tcp", runtimeConfig.Server.ListenAddress)
	if err != nil {
		fatal("grpc_listen_failed", err, "listen_address", runtimeConfig.Server.ListenAddress)
	}
	logs.Default().Info("grpc_server_started", "listen_address", runtimeConfig.Server.ListenAddress)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- grpcServer.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			fatal("grpc_server_failed", err)
		}
	case <-ctx.Done():
		// ACK 的 180 秒终止宽限只有在进程优雅停服时才有效；先摘健康状态，再等待在途 LTX 流结束。
		healthServer.Shutdown()
		grpcServer.GracefulStop()
		if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			logs.Default().Error("grpc_server_shutdown_failed", "error", err)
		}
	}
}

func fatal(message string, err error, attrs ...any) {
	fields := append([]any{"error", err}, attrs...)
	logs.Default().Error(message, fields...)
	os.Exit(1)
}
