package vworldimage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

func TestRegistrationForwardsExplicitRoleWithoutCrop(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("garment_name") != "Black Embellished Sandals" || r.FormValue("garment_role") != "shoes" || r.Form.Has("use_crop") {
			t.Fatalf("fields: %#v", r.Form)
		}
		if r.Form.Has("prompt") || r.Form.Has("parse_mask") || r.Form.Has("parse_labels") {
			t.Fatal("registration must retain server prompt and no parser crop")
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes(t))}}})
	}))
	defer server.Close()
	p := newTestProvider(t, server.URL)
	if _, err := p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), `{"garment_name":"Black Embellished Sandals","garment_role":"shoes"}`)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("generation calls=%d", calls)
	}
}

// The gateway must not duplicate the model server's role enum or reject a new role.
func TestExplicitRolesPassThroughToModelService(t *testing.T) {
	for _, tc := range []struct {
		role, slot string
		crop       bool
	}{
		{"feet", "", false},
		{"footwear.sandals", "", false},
		{"future-role", "", true},
		{"shoes", "top", true},
		{" shoes ", "top", false},
	} {
		t.Run(tc.role+tc.slot, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if err := r.ParseMultipartForm(8 << 20); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				if r.FormValue("garment_role") != tc.role {
					t.Errorf("role=%q, want %q", r.FormValue("garment_role"), tc.role)
				}
				// Upstream owns validation, and the gateway propagates its error.
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"detail":"unsupported garment_role"}`))
			}))
			defer server.Close()
			p := newTestProvider(t, server.URL)
			meta := map[string]any{"garment_name": "shoe", "garment_role": tc.role, "inventory_slot": tc.slot}
			var masks [][]byte
			if tc.crop {
				meta["parse_labels"] = map[string]string{"1": "feet"}
				masks = append(masks, []byte("mask"))
			}
			data, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.GenerateImage(context.Background(), models.VWorldWardrobe10, wardrobeRequest(pngBytes(t), string(data), masks...))
			if calls != 1 || err == nil || provider.IsNotAttempted(err) || !strings.Contains(err.Error(), "unsupported garment_role") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}
