package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestParseArgs(t *testing.T) {
	validArgs := []string{
		"--address", "modelhub.internal.dev:50053",
		"--prompt", "red paper boat",
		"--output", "image.png",
	}

	t.Run("uses required values and defaults", func(t *testing.T) {
		got, failure := parseArgs(validArgs)
		if failure != nil {
			t.Fatalf("parseArgs failure=%v", failure)
		}
		want := clientConfig{
			address: "modelhub.internal.dev:50053",
			prompt:  "red paper boat",
			output:  "image.png",
			timeout: 5 * time.Minute,
			force:   false,
		}
		if got != want {
			t.Fatalf("config=%+v, want %+v", got, want)
		}
	})

	t.Run("accepts positive custom timeout and force", func(t *testing.T) {
		got, failure := parseArgs(append(validArgs, "--timeout", "45s", "--force"))
		if failure != nil {
			t.Fatalf("parseArgs failure=%v", failure)
		}
		if got.timeout != 45*time.Second || !got.force {
			t.Fatalf("config=%+v", got)
		}
	})

	t.Run("preserves non-blank prompt and output values", func(t *testing.T) {
		wantPrompt := "  red paper boat  "
		wantOutput := " image.png "
		got, failure := parseArgs([]string{
			"--address", "modelhub.internal.dev:50053",
			"--prompt", wantPrompt,
			"--output", wantOutput,
		})
		if failure != nil {
			t.Fatalf("parseArgs failure=%v", failure)
		}
		if got.prompt != wantPrompt || got.output != wantOutput {
			t.Fatalf("config=%+v", got)
		}
	})

	invalidCases := []struct {
		name string
		args []string
	}{
		{name: "missing address", args: []string{"--prompt", "red paper boat", "--output", "image.png"}},
		{name: "missing prompt", args: []string{"--address", "modelhub.internal.dev:50053", "--output", "image.png"}},
		{name: "missing output", args: []string{"--address", "modelhub.internal.dev:50053", "--prompt", "red paper boat"}},
		{name: "unknown flag", args: append(validArgs, "--unknown")},
		{name: "blank address", args: []string{"--address", "   ", "--prompt", "red paper boat", "--output", "image.png"}},
		{name: "blank prompt", args: []string{"--address", "modelhub.internal.dev:50053", "--prompt", "   ", "--output", "image.png"}},
		{name: "blank output", args: []string{"--address", "modelhub.internal.dev:50053", "--prompt", "red paper boat", "--output", "   "}},
		{name: "zero timeout", args: append(validArgs, "--timeout", "0s")},
		{name: "negative timeout", args: append(validArgs, "--timeout", "-1s")},
		{name: "positional argument", args: append(validArgs, "extra")},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			_, failure := parseArgs(test.args)
			if failure == nil || failure.category != failureSourceValidation {
				t.Fatalf("failure=%v", failure)
			}
			if strings.Contains(failure.Error(), "red paper boat") {
				t.Fatalf("failure exposed prompt: %q", failure.Error())
			}
		})
	}
}

func TestRun(t *testing.T) {
	const prompt = "secret prompt must never reach output"
	const providerBody = "fake provider body must never reach output"
	const imageBody = "raw-image-bytes-must-never-reach-output"
	args := []string{
		"--address", "bufnet",
		"--prompt", prompt,
		"--output", filepath.Join(t.TempDir(), "image.png"),
	}

	t.Run("writes a large valid image and emits only a safe summary", func(t *testing.T) {
		data := bytes.Repeat([]byte("a"), 4<<20+1)
		service := &recordingService{events: []*modelhubv2.GenerateEvent{{
			Final: true,
			Items: []*modelhubv2.OutputItem{inlineImage("image/png", data)},
		}}}
		dial := newRunTestDialer(t, service)
		var output bytes.Buffer
		failure := run(context.Background(), args, &output, dial)
		if failure != nil {
			t.Fatalf("run failure=%v", failure)
		}
		wantOutput := "mime_type=image/png bytes=4194305 output=" + args[5] + "\n"
		if output.String() != wantOutput {
			t.Fatalf("output=%q, want %q", output.String(), wantOutput)
		}
		got, err := os.ReadFile(args[5])
		if err != nil {
			t.Fatalf("read output: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("output image differs")
		}
	})

	failureCases := []struct {
		name     string
		args     []string
		dial     func(*testing.T) dialContextFunc
		category failureCategory
	}{
		{
			name: "source validation",
			args: []string{"--address", "bufnet", "--prompt", prompt, "--output", "   "},
			dial: func(*testing.T) dialContextFunc {
				return func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error) {
					t.Fatal("dial called")
					return nil, nil
				}
			},
			category: failureSourceValidation,
		},
		{
			name: "connection failure",
			dial: func(*testing.T) dialContextFunc {
				return func(context.Context, string, ...grpc.DialOption) (*grpc.ClientConn, error) {
					return nil, errors.New(providerBody)
				}
			},
			category: failureConnect,
		},
		{
			name: "dial timeout",
			args: append(args, "--timeout", "1ms"),
			dial: func(*testing.T) dialContextFunc {
				return func(ctx context.Context, _ string, _ ...grpc.DialOption) (*grpc.ClientConn, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}
			},
			category: failureTimeout,
		},
		{
			name: "rpc failure",
			dial: func(t *testing.T) dialContextFunc {
				return newRunTestDialer(t, &recordingService{err: status.Error(codes.Internal, providerBody)})
			},
			category: failureRPC,
		},
		{
			name: "protocol failure",
			dial: func(t *testing.T) dialContextFunc {
				return newRunTestDialer(t, &recordingService{events: []*modelhubv2.GenerateEvent{{Final: true}}})
			},
			category: failureProtocol,
		},
		{
			name: "output failure",
			args: []string{"--address", "bufnet", "--prompt", prompt, "--output", filepath.Join(t.TempDir(), "missing", "image.png")},
			dial: func(t *testing.T) dialContextFunc {
				return newRunTestDialer(t, &recordingService{events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{inlineImage("image/png", []byte(imageBody))}}}})
			},
			category: failureOutput,
		},
	}
	for _, test := range failureCases {
		t.Run(test.name, func(t *testing.T) {
			caseArgs := test.args
			if caseArgs == nil {
				caseArgs = args
			}
			var output bytes.Buffer
			failure := run(context.Background(), caseArgs, &output, test.dial(t))
			if failure == nil || failure.category != test.category {
				t.Fatalf("failure=%v", failure)
			}
			for _, secret := range []string{prompt, providerBody, imageBody} {
				if strings.Contains(output.String(), secret) || strings.Contains(failure.Error(), secret) {
					t.Fatalf("output exposed %q: output=%q failure=%q", secret, output.String(), failure.Error())
				}
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	cases := map[failureCategory]int{
		failureSourceValidation: 2,
		failureConnect:          20,
		failureRPC:              21,
		failureTimeout:          22,
		failureProtocol:         23,
		failureOutput:           24,
	}
	for category, want := range cases {
		if got := exitCode(&smokeFailure{category: category}); got != want {
			t.Fatalf("exitCode(%q)=%d, want %d", category, got, want)
		}
	}
}

func newRunTestDialer(t *testing.T, service *recordingService) dialContextFunc {
	t.Helper()
	listener := bufconn.Listen(bufSize)
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(protocol.MaxRPCMessageBytes),
		grpc.MaxSendMsgSize(protocol.MaxRPCMessageBytes),
	)
	modelhubv2.RegisterModelHubServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return func(ctx context.Context, target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
		options = append(options, grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}))
		return grpc.DialContext(ctx, target, options...)
	}
}
