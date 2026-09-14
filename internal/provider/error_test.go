package provider

import (
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
