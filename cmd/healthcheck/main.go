package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "healthcheck failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("wg-model-hub-healthcheck", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("address", "127.0.0.1:50053", "gRPC server address")
	timeout := flags.Duration("timeout", 3*time.Second, "health check timeout")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if strings.TrimSpace(*address) == "" {
		return fmt.Errorf("address is required")
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}

	checkCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	conn, err := grpc.NewClient(*address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("create gRPC client: %w", err)
	}
	defer conn.Close()
	response, err := grpc_health_v1.NewHealthClient(conn).Check(
		checkCtx,
		&grpc_health_v1.HealthCheckRequest{},
	)
	if err != nil {
		return fmt.Errorf("check gRPC health: %w", err)
	}
	if response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		return fmt.Errorf("gRPC health status is %s", response.GetStatus())
	}
	return nil
}
