package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type clientConfig struct {
	address string
	prompt  string
	output  string
	timeout time.Duration
	force   bool
}

type dialContextFunc func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error)

func main() {
	if failure := run(context.Background(), os.Args[1:], os.Stdout, grpc.DialContext); failure != nil {
		fmt.Fprintln(os.Stderr, failure.Error())
		os.Exit(exitCode(failure))
	}
}

func parseArgs(args []string) (clientConfig, *smokeFailure) {
	flags := flag.NewFlagSet("gpt-image-2", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	address := flags.String("address", "", "")
	prompt := flags.String("prompt", "", "")
	output := flags.String("output", "", "")
	timeout := flags.Duration("timeout", 5*time.Minute, "")
	force := flags.Bool("force", false, "")
	if flags.Parse(args) != nil || len(flags.Args()) != 0 {
		return clientConfig{}, &smokeFailure{category: failureSourceValidation}
	}
	config := clientConfig{
		address: *address,
		prompt:  *prompt,
		output:  *output,
		timeout: *timeout,
		force:   *force,
	}
	if strings.TrimSpace(config.address) == "" || strings.TrimSpace(config.prompt) == "" || strings.TrimSpace(config.output) == "" || config.timeout <= 0 {
		return clientConfig{}, &smokeFailure{category: failureSourceValidation}
	}
	return config, nil
}

func run(ctx context.Context, args []string, output io.Writer, dial dialContextFunc) *smokeFailure {
	config, failure := parseArgs(args)
	if failure != nil {
		return failure
	}
	callContext, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()
	conn, err := dial(
		callContext,
		config.address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDisableRetry(),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(protocol.MaxRPCMessageBytes)),
	)
	if err != nil {
		if callContext.Err() != nil {
			return &smokeFailure{category: failureTimeout}
		}
		return &smokeFailure{category: failureConnect}
	}
	defer conn.Close()
	result, failure := generateImage(callContext, modelhubv2.NewModelHubServiceClient(conn), config.prompt)
	if failure != nil {
		return failure
	}
	if failure := writeImage(config.output, result.data, config.force); failure != nil {
		return failure
	}
	_, _ = fmt.Fprintf(output, "mime_type=%s bytes=%d output=%s\n", result.mimeType, len(result.data), config.output)
	return nil
}

func exitCode(failure *smokeFailure) int {
	switch failure.category {
	case failureSourceValidation:
		return 2
	case failureConnect:
		return 20
	case failureRPC:
		return 21
	case failureTimeout:
		return 22
	case failureProtocol:
		return 23
	case failureOutput:
		return 24
	default:
		return 1
	}
}
