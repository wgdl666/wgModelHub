package vworldimage

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/models"
)

func TestGatewayImageSendsSchemaFields(t *testing.T) {
	source := pngBytes(t)
	bodyRequest := wardrobeRequest(source, "ignored body prompt")
	bodyRequest.Input.ModelTraits = `{"gender":"female"}`
	for _, tc := range []struct {
		name, model string
		request     *modelhubv2.GenerateRequest
		fields      map[string]any
		images      [][]byte
	}{
		{"vton", models.VWorldOutfit10, outfitRequest(source, []byte("garment"), " preserve prompt "), map[string]any{"prompt": " preserve prompt "}, [][]byte{source, []byte("garment")}},
		{"product", models.VWorldClothGen10, wardrobeRequest(source, `{"garment_name":"shirt","garment_role":"tops"}`), map[string]any{"garment_name": "shirt", "garment_role": "tops"}, [][]byte{source}},
		{"extraction bbox", models.VWorldWardrobe10, wardrobeRequest(source, `{"garment_name":"pants","bbox":[1,2,3,4],"bbox_format":"xyxy"}`), map[string]any{"garment_name": "pants", "bbox": []any{float64(1), float64(2), float64(3), float64(4)}, "bbox_format": "xyxy"}, [][]byte{source}},
		{"extraction mask", models.VWorldWardrobe10, wardrobeRequest(source, `{"garment_name":"shirt","parse_labels":{"15":"feet"}}`, []byte("mask")), map[string]any{"garment_name": "shirt", "parse_labels": map[string]any{"15": "feet"}}, [][]byte{source, []byte("mask")}},
		{"body", models.VWorldBodyModel10, bodyRequest, map[string]any{"gender": "female"}, [][]byte{source}},
		// Product/extraction rules belong to Gateway Schema, so the adapter forwards these fields for rejection there.
		{"product extraction fields", models.VWorldClothGen10, wardrobeRequest(source, `{"bbox":[1,2,3,4]}`), map[string]any{"garment_name": "", "bbox": []any{float64(1), float64(2), float64(3), float64(4)}}, [][]byte{source}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received map[string]any
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("incorrect Gateway authentication")
				}
				if r.Method == http.MethodGet {
					if r.URL.Path != "/v1/models/"+tc.model {
						t.Error("incorrect definition lookup")
					}
					kind := map[string]string{models.VWorldOutfit10: "vton", models.VWorldClothGen10: "product_generation", models.VWorldWardrobe10: "garment_extraction", models.VWorldBodyModel10: "body_model"}[tc.model]
					_ = json.NewEncoder(w).Encode(map[string]string{"protocol": kind})
					return
				}
				if r.URL.Path != "/v1/gateway/requests/"+tc.model || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("incorrect Gateway target or authentication")
				}
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer gateway.Close()
			client, err := vmindgateway.New("fixture", gateway.URL, "fixture-token", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewGateway("fixture", client).GenerateImage(t.Context(), tc.model, tc.request); err == nil {
				t.Fatal("Gateway rejection was not propagated")
			}
			images := make([]any, len(tc.images))
			for i, data := range tc.images {
				images[i] = map[string]any{"mime_type": "image/png", "data": base64.StdEncoding.EncodeToString(data)}
			}
			tc.fields["images"] = images
			if received["output"] != "image" || !reflect.DeepEqual(received["input"], tc.fields) {
				t.Fatalf("canonical input changed: got %#v, want %#v", received, tc.fields)
			}
		})
	}
}

func TestGatewayImageDefinitionFailureDoesNotSubmit(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"missing model", `{"error":{"code":"model_not_found"}}`, http.StatusNotFound},
		{"invalid definition", `{`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("submitted after model definition failure")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer gateway.Close()
			client, err := vmindgateway.New("fixture", gateway.URL, "fixture-token", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewGateway("fixture", client).GenerateImage(t.Context(), models.VWorldOutfit10, &modelhubv2.GenerateRequest{}); err == nil {
				t.Fatal("model definition failure was not propagated")
			}
		})
	}
}
