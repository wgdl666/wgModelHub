package provider

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFromHTTPDetailKeepsVendorSnippet(t *testing.T) {
	err := FromHTTPDetail("hub_dashscope", http.StatusBadRequest, `{"error":{"message":"tool_choice is invalid"}}`)
	if err == nil || Kind(err) != ErrorInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "hub_dashscope returned HTTP 400") {
		t.Fatalf("status missing: %v", err)
	}
	if !strings.Contains(err.Error(), "tool_choice is invalid") {
		t.Fatalf("vendor snippet missing: %v", err)
	}
}

func TestFromHTTPDetailIsAttempted(t *testing.T) {
	err := FromHTTPDetail("hub", http.StatusBadRequest, `{"error":"bad"}`)
	if IsNotAttempted(err) {
		t.Fatal("HTTP error must count as attempted")
	}
	if Kind(err) != ErrorInvalidArgument {
		t.Fatalf("kind=%s", Kind(err))
	}
}

func TestWrapNotAttemptedAndAsNotAttempted(t *testing.T) {
	err := WrapNotAttempted(ErrorInvalidArgument, "create request failed", errors.New("bad url"))
	if !IsNotAttempted(err) {
		t.Fatal("WrapNotAttempted must be local")
	}
	httpErr := FromHTTPDetail("hub", http.StatusBadRequest, "x")
	if IsNotAttempted(AsNotAttempted(httpErr)) == false {
		t.Fatal("AsNotAttempted should mark local")
	}
	// 原始 FromHTTPDetail 语义不变：已触达供应商的 HTTP400 仍为 attempted。
	if IsNotAttempted(httpErr) {
		t.Fatal("FromHTTPDetail must stay attempted")
	}
}

func TestNewIsAttemptedNotAttemptedIsLocal(t *testing.T) {
	// New/Errorf 保持正常语义：不能仅因 InvalidArgument 就当成未调供应商。
	if IsNotAttempted(New(ErrorInvalidResponse, "photoroom returned empty image")) {
		t.Fatal("post-response New must be attempted")
	}
	if IsNotAttempted(New(ErrorInvalidArgument, "vendor said bad request after HTTP")) {
		t.Fatal("New InvalidArgument must not imply not-attempted")
	}
	local := NotAttempted(ErrorInvalidArgument, "image prompt is required")
	if !IsNotAttempted(local) {
		t.Fatal("NotAttempted must be local")
	}
	wrapped := Wrap(ErrorInvalidArgument, "validate", local)
	if !IsNotAttempted(wrapped) {
		t.Fatal("wrap must preserve local")
	}
}

func TestCompactHTTPErrorDetailTruncatesAndStripsControl(t *testing.T) {
	if got := CompactHTTPErrorDetail("  a\n\tb  "); got != "a b" {
		t.Fatalf("compact = %q", got)
	}
	long := strings.Repeat("x", httpErrorDetailLimit+8)
	got := CompactHTTPErrorDetail(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated = %q", got)
	}
	if strings.Count(got, "x") != httpErrorDetailLimit {
		t.Fatalf("rune count = %d", strings.Count(got, "x"))
	}
}
