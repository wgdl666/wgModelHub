package publicrpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type stubHub struct {
	modelhubv2.UnimplementedModelHubServiceServer
}

func (stubHub) ListModels(context.Context, *modelhubv2.ListModelsRequest) (*modelhubv2.ListModelsResponse, error) {
	return &modelhubv2.ListModelsResponse{Models: []*modelhubv2.ModelInfo{{
		Model:    "gemini-3.7-flash",
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_LLM,
	}}}, nil
}

func testPublicHandler() http.Handler {
	server := grpc.NewServer()
	modelhubv2.RegisterModelHubServiceServer(server, stubHub{})
	return Handler(server)
}

func TestAllowOrigin(t *testing.T) {
	if !AllowOrigin("https://ops.wgdl.tech") {
		t.Fatal("ops origin must be allowed")
	}
	if AllowOrigin("https://evil.example") {
		t.Fatal("unknown origin must be rejected")
	}
}

func TestGrpcWebCORSPreflight(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/wg_model_hub.v2.ModelHubService/ListModels", nil)
	req.Header.Set("Origin", "https://ops.wgdl.tech")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-grpc-web")
	rec := httptest.NewRecorder()
	testPublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://ops.wgdl.tech" {
		t.Fatalf("allow-origin=%q", got)
	}
}

func TestGrpcWebListModels(t *testing.T) {
	payload, err := proto.Marshal(&modelhubv2.ListModelsRequest{
		Category: modelhubv2.ModelCategory_MODEL_CATEGORY_LLM,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(payload)))
	copy(frame[5:], payload)

	req := httptest.NewRequest(http.MethodPost, "/wg_model_hub.v2.ModelHubService/ListModels", bytes.NewReader(frame))
	req.Header.Set("Content-Type", "application/grpc-web+proto")
	req.Header.Set("X-Grpc-Web", "1")
	req.Header.Set("Origin", "https://ops.wgdl.tech")
	rec := httptest.NewRecorder()
	testPublicHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp := &modelhubv2.ListModelsResponse{}
	if err := proto.Unmarshal(grpcWebData(t, body), resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetModels()) != 1 || resp.GetModels()[0].GetModel() != "gemini-3.7-flash" {
		t.Fatalf("resp=%v", resp)
	}
}

func grpcWebData(t *testing.T, body []byte) []byte {
	t.Helper()
	offset := 0
	var data []byte
	for offset+5 <= len(body) {
		flags := body[offset]
		size := int(binary.BigEndian.Uint32(body[offset+1 : offset+5]))
		offset += 5
		if offset+size > len(body) {
			t.Fatalf("truncated grpc-web frame")
		}
		chunk := body[offset : offset+size]
		offset += size
		if flags&0x80 != 0 {
			if !strings.Contains(string(chunk), "grpc-status: 0") {
				t.Fatalf("trailers=%q", chunk)
			}
			continue
		}
		data = append(data, chunk...)
	}
	return data
}
