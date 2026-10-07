package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
)

type storageRoundTrip func(*http.Request) (*http.Response, error)

func (f storageRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// 使用真实 SDK 构造并签名请求，验证曾导致 OSS 403 的 Host 与对象路径。
func TestOfficialProviderUploadAddress(t *testing.T) {
	for _, tc := range []struct{ provider, endpoint, region, signature string }{
		{"oss", "https://oss-cn-shenzhen.aliyuncs.com", "cn-shenzhen", "OSS4-HMAC-SHA256"},
		{"s3", "https://s3.ap-southeast-1.amazonaws.com", "ap-southeast-1", "AWS4-HMAC-SHA256"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			original := http.DefaultTransport
			defer func() { http.DefaultTransport = original }()
			calls := 0
			http.DefaultTransport = storageRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				endpoint, _ := url.Parse(tc.endpoint)
				if r.URL.Host != "media-test."+endpoint.Host || r.URL.Path != "/model-calls/call/out/0" {
					t.Fatalf("wrong addressing: %s", r.URL)
				}
				if !strings.HasPrefix(r.Header.Get("Authorization"), tc.signature) {
					t.Fatalf("wrong signing scheme")
				}
				data, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(data), "image-data") || r.Header.Get("Content-Type") != "image/png" {
					t.Fatalf("body/content type changed")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Etag": []string{"test"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
			})
			store, err := New(context.Background(), config.ObjectStorageConfig{Provider: tc.provider, Endpoint: tc.endpoint, Region: tc.region, Bucket: "media-test", AccessKeyID: "test-ak", AccessKeySecret: "test-secret"})
			if err != nil {
				t.Fatal(err)
			}
			uri, err := store.Put(context.Background(), "model-calls/call/out/0", "image/png", []byte("image-data"))
			if err != nil || uri != "s3://media-test/model-calls/call/out/0" || calls != 1 {
				t.Fatalf("uri=%s calls=%d err=%v", uri, calls, err)
			}
		})
	}
}
