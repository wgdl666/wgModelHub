package provider

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestToStatusKeepsWrappedVendorReason(t *testing.T) {
	err := Wrap(ErrorUnavailable, "rekognition_person detect labels", errors.New("InvalidImageException: Request has invalid image format"))
	st, ok := status.FromError(ToStatus(err))
	if !ok {
		t.Fatal("expected grpc status")
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("code = %s", st.Code())
	}
	if !strings.Contains(st.Message(), "rekognition_person detect labels") || !strings.Contains(st.Message(), "InvalidImageException: Request has invalid image format") {
		t.Fatalf("message = %s", st.Message())
	}
}
