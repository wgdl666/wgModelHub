package vworldimage

import (
	"errors"
	"os"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

// Run by modelGateway's cross-repository contract test against its real HTTP
// handler, database model definitions and schema admission (no GPU required).
func TestGatewaySchemaContract(t *testing.T) {
	base := os.Getenv("VMIND_GATEWAY_TEST_URL")
	if base == "" {
		t.Skip("VMIND_GATEWAY_TEST_URL required")
	}
	client, err := vmindgateway.New("contract", base, os.Getenv("VMIND_GATEWAY_TEST_TOKEN"), nil)
	if err != nil {
		t.Fatal(err)
	}
	source := pngBytes(t)
	body := wardrobeRequest(source, "")
	body.Input.ModelTraits = `{"gender":"invalid"}`
	for _, tc := range []struct {
		model string
		body  bool
	}{{models.VWorldBodyModel10, true}, {models.VWorldOutfit10, false}} {
		request := body
		if !tc.body {
			request = outfitRequest(source, source, "")
		}
		_, err := NewGateway("contract", client).GenerateImage(t.Context(), tc.model, request)
		var rejected *provider.Error
		if !errors.As(err, &rejected) || rejected.Kind != provider.ErrorInvalidArgument || rejected.Local {
			t.Fatalf("%s: expected remote schema rejection, got %v", tc.model, err)
		}
	}
}
