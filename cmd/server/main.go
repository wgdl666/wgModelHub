package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/config"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/apikeystore"
	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/infra/factory"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/service/modelhub"
	"github.com/wgdl666/wgModelHub/internal/taskstore"
	"github.com/wgdl666/wgModelHub/protocol"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	configLoader, err := config.NewRuntimeLoaderFromEnv()
	if err != nil {
		fatal("runtime_config_loader_init_failed", err)
	}
	defer configLoader.Close()

	runtimeConfig, _, err := configLoader.Load(ctx)
	if err != nil {
		fatal("runtime_config_load_failed", err)
	}
	if err := config.ApplyListenPortOverridesFromEnv(&runtimeConfig); err != nil {
		fatal("listen_port_env_failed", err)
	}

	live := config.NewLiveConfig(runtimeConfig)
	if err := configLoader.Listen(func(_, _, data string) {
		live.ApplyYAML(data)
	}); err != nil {
		fatal("runtime_config_listen_failed", err)
	}

	telemetryRuntime, err := telemetry.Setup(ctx, runtimeConfig.Logfire)
	if err != nil {
		fatal("telemetry_setup_failed", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = telemetryRuntime.Shutdown(shutdownCtx)
	}()

	providers, err := factory.Build(ctx, runtimeConfig)
	if err != nil {
		fatal("provider_factory_failed", err)
	}

	entClient, err := taskstore.Open(ctx, runtimeConfig.Database.DSN)
	if err != nil {
		fatal("database_connect_failed", err)
	}
	defer entClient.Close()

	apiKeys := apikeystore.New(entClient)
	hubService := modelhub.New(live, providers, taskstore.NewPostgres(entClient))

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	var grpcServers []*grpc.Server
	serveErr := make(chan error, 2)
	startServer := func(name, addr string, opts ...grpc.ServerOption) {
		if strings.TrimSpace(addr) == "" {
			return
		}
		base := []grpc.ServerOption{
			grpc.MaxRecvMsgSize(protocol.MaxRPCMessageBytes),
			grpc.MaxSendMsgSize(protocol.MaxRPCMessageBytes),
			grpc.StatsHandler(otelgrpc.NewServerHandler()),
		}
		grpcServer := grpc.NewServer(append(base, opts...)...)
		grpcServers = append(grpcServers, grpcServer)
		modelhubv2.RegisterModelHubServiceServer(grpcServer, hubService)
		grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
		listener, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			fatal("grpc_listen_failed", listenErr, "server", name, "listen_address", addr)
		}
		logs.Default().Info("grpc_server_started", "server", name, "listen_address", addr)
		go func() {
			serveErr <- grpcServer.Serve(listener)
		}()
	}

	startServer("intranet", runtimeConfig.Server.ListenAddress)
	publicAddr := strings.TrimSpace(runtimeConfig.Server.PublicListenAddress)
	if publicAddr != "" {
		// 公网 listener 与内网分离：按 metadata Bearer 鉴权，health 仍放行供 K8s/反代探测。
		startServer("public", publicAddr,
			grpc.UnaryInterceptor(auth.UnaryServerInterceptor(apiKeys)),
			grpc.StreamInterceptor(auth.StreamServerInterceptor(apiKeys)),
		)
	}

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			fatal("grpc_server_failed", err)
		}
	case <-ctx.Done():
		healthServer.Shutdown()
		for _, srv := range grpcServers {
			srv.GracefulStop()
		}
		for range grpcServers {
			if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				logs.Default().Error("grpc_server_shutdown_failed", "error", err)
			}
		}
	}
}

func fatal(message string, err error, attrs ...any) {
	fields := append([]any{"error", err}, attrs...)
	logs.Default().Error(message, fields...)
	os.Exit(1)
}
