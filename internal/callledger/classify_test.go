package callledger

import (
	"context"
	"net/http"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/provider"
)

func TestClassifyErrorDoesNotBlameCallerForHTTP400(t *testing.T) {
	err := provider.FromHTTPDetail("p", http.StatusBadRequest, "nope")
	cat, _, _, _ := ClassifyError(err)
	if cat != ErrorCategoryUnknown {
		t.Fatalf("category=%q want unknown", cat)
	}
}

func TestClassifyErrorLocalInvalidArgumentIsCaller(t *testing.T) {
	err := provider.NotAttempted(provider.ErrorInvalidArgument, "model is required")
	cat, _, _, _ := ClassifyError(err)
	if cat != ErrorCategoryCallerRequest {
		t.Fatalf("category=%q", cat)
	}
}

func TestClassifyErrorCancelled(t *testing.T) {
	cat, _, _, _ := ClassifyError(context.Canceled)
	if cat != ErrorCategoryCancelled {
		t.Fatalf("category=%q", cat)
	}
}

func TestUsageFromProtoDistinguishesMissingAndZero(t *testing.T) {
	u, detail := UsageFromProto(nil)
	if u != nil || detail["usage_present"] != false {
		t.Fatalf("missing usage=%v detail=%v", u, detail)
	}
}
