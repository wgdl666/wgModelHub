package segmentperson

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wgdl666/wgModelHub/models"
)

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSegmentTransientRetry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failure    error
		status     int
		persistent bool
		want       int
	}{
		{name: "broken pipe recovers", failure: syscall.EPIPE, want: 2},
		{name: "reset budget", failure: syscall.ECONNRESET, persistent: true, want: 3},
		{name: "EOF recovers", failure: io.EOF, want: 2},
		{name: "unavailable recovers", status: 503, want: 2},
		{name: "rate limited budget", status: 429, persistent: true, want: 3},
		{name: "bad request", status: 400, persistent: true, want: 1},
		{name: "unauthorized", status: 401, persistent: true, want: 1},
		{name: "cancelled", failure: context.Canceled, persistent: true, want: 1},
		{name: "deadline", failure: context.DeadlineExceeded, persistent: true, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, model := range []string{models.SegmentPersonBria, models.SegmentSubjectBria} {
				p := newTestProvider(t, "http://segment.test", "user", "secret")
				calls := 0
				p.client.Transport = retryTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					// 重试必须重建完整表单，不能只验证调用次数而漏掉空请求体。
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					if r.FormValue("image") != "aW1hZ2U=" || r.FormValue("output") != "b64" {
						t.Fatal("request body was not replayed")
					}
					if calls == 1 || tc.persistent {
						if tc.failure != nil {
							return nil, tc.failure
						}
						return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("error")), Header: make(http.Header)}, nil
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"refined_png_b64":"cG5n"}`)), Header: make(http.Header)}, nil
				})
				_, err := p.GenerateImage(context.Background(), model, imageRequest(inlinePNG([]byte("image"))))
				if calls != tc.want {
					t.Fatalf("calls=%d want %d", calls, tc.want)
				}
				if (err != nil) != tc.persistent {
					t.Fatalf("err=%v", err)
				}
			}
		})
	}
}

func TestSegmentCancellationDuringBackoff(t *testing.T) {
	p := newTestProvider(t, "http://segment.test", "", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	p.client.Transport = retryTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		// 在退避期间取消，确保不再向上游发起第二次请求。
		time.AfterFunc(20*time.Millisecond, cancel)
		return nil, syscall.EPIPE
	})
	_, err := p.GenerateImage(ctx, models.SegmentPersonBria, imageRequest(inlinePNG([]byte("image"))))
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestSegmentRejectsWithoutSending(t *testing.T) {
	p := newTestProvider(t, "http://segment.test", "", "")
	p.client.Transport = retryTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid or cancelled request must not reach provider")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.GenerateImage(ctx, models.SegmentPersonBria, imageRequest(inlinePNG([]byte("image")))); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := p.GenerateImage(context.Background(), "invalid-model", imageRequest(inlinePNG([]byte("image")))); err == nil {
		t.Fatal("expected model validation error")
	}
}
