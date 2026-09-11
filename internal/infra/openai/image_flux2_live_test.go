package openai

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestLiveFlux2Klein9BImageEdit(t *testing.T) {
	// 默认单测不打供应商；接入验收时用真实 FLUX.2-klein-9B i2i 端点走 ModelHub openai.Provider。
	if os.Getenv("WG_MODELHUB_FLUX2_LIVE") != "1" {
		t.Skip("set WG_MODELHUB_FLUX2_LIVE=1 to call FLUX.2-klein-9B image edit")
	}
	baseURL := os.Getenv("FLUX2_KLEIN_BASE_URL")
	inputPath := os.Getenv("FLUX2_KLEIN_INPUT")
	outDir := os.Getenv("WG_MODELHUB_LIVE_OUT")
	if baseURL == "" || inputPath == "" || outDir == "" {
		t.Fatal("FLUX2_KLEIN_BASE_URL, FLUX2_KLEIN_INPUT and WG_MODELHUB_LIVE_OUT are required for live test")
	}
	inputData, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}

	p, err := New("flux2_klein_image", "placeholder-not-validated", baseURL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	event, err := p.GenerateImage(ctx, models.Flux2Klein9B, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{
					{Content: &modelhubv2.ContentPart_Text{Text: "Apply a soft watercolor style while keeping the subject recognizable."}},
					{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
						MimeType: "image/png",
						Source:   &modelhubv2.Media_Data{Data: inputData},
					}}},
				},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var image *modelhubv2.Media
	for _, item := range event.GetItems() {
		if media := item.GetImage(); media != nil && len(media.GetData()) > 0 {
			image = media
			break
		}
	}
	if image == nil {
		t.Fatalf("no image in live response: items=%d", len(event.GetItems()))
	}
	data := image.GetData()
	if len(data) == 0 {
		t.Fatal("image data is empty")
	}
	if image.GetMimeType() != "image/png" {
		t.Fatalf("unexpected mime type: %q", image.GetMimeType())
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("image data is not a PNG")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(outDir, "modelhub-flux2-klein-9b.png")
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved %s mime=%s bytes=%d", outPath, image.GetMimeType(), len(data))
}
