package vmindgateway

import (
	"context"
	"encoding/json"
	"github.com/wgdl666/kangaroo/callmeta"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestSubmitPreservesImageJSONAndCallerInput(t *testing.T) {
	for _, images := range [][]Image{nil, {}, {{MIMEType: "image/png", Data: []byte{0, 255}}, {MIMEType: "image/jpeg", Data: []byte("jpeg")}}} {
		input := map[string]any{"images": images, "prompt": "衣服 <&>"}
		var received map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(callmeta.DeviceID) != "device-a" || r.Header.Get(callmeta.UserID) != "user-a" {
				t.Error("gateway identity missing")
			}
			if r.Header.Get("Authorization") != "Bearer local" || r.Header.Get("Idempotency-Key") != "id" {
				t.Error("authentication or request ID changed")
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"request_id":"id","state":"queued"}`)
		}))
		client := &Client{name: "local", baseURL: server.URL, token: "local", http: server.Client()}
		_, err := client.Submit(callmeta.WithIdentity(context.Background(), "device-a", "user-a"), "model", "id", input, http.StatusAccepted)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		expected, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err := json.Unmarshal(expected, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(received["input"], decoded) {
			t.Fatalf("input changed: %v != %v", received["input"], decoded)
		}
		if _, ok := input["images"].([]Image); !ok {
			t.Fatal("caller input was mutated")
		}
	}
}

func TestGetSendsEmptyBody(t *testing.T) {
	const id = "123e4567-e89b-12d3-a456-426614174000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodGet || len(body) != 0 || r.ContentLength != 0 {
			t.Error("GET must keep an empty body")
		}
		_, _ = io.WriteString(w, `{"request_id":"`+id+`","state":"processing"}`)
	}))
	defer server.Close()
	client := &Client{name: "local", baseURL: server.URL, token: "local", http: server.Client()}
	if _, err := client.Get(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}
