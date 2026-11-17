package vmindgateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	oss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aws/smithy-go"
	"github.com/wgdl666/kangaroo/bizerr"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/infra/objectstore"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type resultTransport func(*http.Request) (*http.Response, error)

func (f resultTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 真实 SDK 解析带敏感正文的拒绝响应，覆盖 OpenResult 到 RPC 的完整错误路径。
func TestOpenResultPreservesStorageErrorWithoutLeaking(t *testing.T) {
	const id = "3988e4bc-204e-4797-ae88-4d3914f560d8"
	for _, backend := range []string{"s3", "oss"} {
		t.Run(backend, func(t *testing.T) {
			var logs bytes.Buffer
			previousLogger, previousTransport := slog.Default(), http.DefaultTransport
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer func() { slog.SetDefault(previousLogger); http.DefaultTransport = previousTransport }()
			http.DefaultTransport = resultTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 403, Request: r, Header: http.Header{"Content-Type": {"application/xml"}, "X-Amz-Request-Id": {"storage-request-1"}}, Body: io.NopCloser(strings.NewReader(`<Error><Code>AccessDenied</Code><Message>secret-token https://private/path?signature=secret</Message><RequestId>storage-request-1</RequestId></Error>`))}, nil
			})
			objects, err := objectstore.New(context.Background(), config.ObjectStorageConfig{Provider: backend, Region: "cn-shenzhen", Bucket: "private-bucket", Endpoint: "https://storage.example.com", AccessKeyID: "test-key", AccessKeySecret: "secret-key"})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{objects: objects}
			_, _, err = client.OpenResult(context.Background(), id, objectstore.Reference{Provider: backend, Region: "cn-shenzhen", Bucket: "private-bucket", Key: "outputs/" + id + "/private-image.png"})
			if err == nil {
				t.Fatal("expected storage failure")
			}
			var businessError *bizerr.Error
			if !errors.As(err, &businessError) || businessError.Code() != provider.CodeResultObjectRead {
				t.Fatalf("business error lost: %T", err)
			}
			if backend == "s3" {
				var sdkError smithy.APIError
				if !errors.As(err, &sdkError) || sdkError.ErrorCode() != "AccessDenied" {
					t.Fatal("S3 error chain lost")
				}
			} else {
				var sdkError *oss.ServiceError
				if !errors.As(err, &sdkError) || sdkError.Code != "AccessDenied" {
					t.Fatal("OSS error chain lost")
				}
			}
			rpc := status.Convert(provider.ToStatus(err))
			if rpc.Code() != codes.Unavailable || rpc.Message() != "cannot open VMind result object" {
				t.Fatalf("RPC contract changed: %v", rpc)
			}
			output := logs.String() + err.Error() + rpc.Message()
			for _, secret := range []string{"secret-token", "signature", "private-bucket", "private-image", "storage.example.com", "secret-key"} {
				if strings.Contains(output, secret) {
					t.Fatalf("sensitive text leaked: %s", secret)
				}
			}
			for _, expected := range []string{id, "open_reference", "AccessDenied", "403", "storage-request-1"} {
				if !strings.Contains(logs.String(), expected) {
					t.Fatalf("missing diagnostic %s: %s", expected, logs.String())
				}
			}
		})
	}
}

func TestResultOpenErrorContextAndRedaction(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	for _, tc := range []struct {
		cause error
		code  codes.Code
	}{{context.Canceled, codes.Canceled}, {context.DeadlineExceeded, codes.DeadlineExceeded}} {
		err := resultOpenError(context.Background(), "not-a-uuid-secret", tc.cause)
		if !errors.Is(err, tc.cause) {
			t.Fatal("context cause lost")
		}
		if status.Code(provider.ToStatus(err)) != tc.code {
			t.Fatal("context status lost")
		}
	}
	if strings.Contains(logs.String(), "cancelled") {
		t.Fatal("caller cancellation logged at warning level")
	}
	if strings.Contains(logs.String(), "not-a-uuid-secret") {
		t.Fatal("unvalidated request ID leaked")
	}
	injected := &oss.ServiceError{Code: "secret\ncode", RequestID: "https://secret?token=secret", StatusCode: 403, Message: "secret-message"}
	err := resultOpenError(context.Background(), "", injected)
	if !errors.Is(err, injected) {
		t.Fatal("original error identity lost")
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("unsafe diagnostic leaked")
	}
}
