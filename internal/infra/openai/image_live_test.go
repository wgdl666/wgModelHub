package openai

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

func TestLiveOminiLinkGPTImage2(t *testing.T) {
	// 默认单测不打供应商；接入自测时用 Muse ominilink 凭据走真实 Images API。
	if os.Getenv("WG_MODELHUB_LIVE") != "1" {
		t.Skip("set WG_MODELHUB_LIVE=1 to call OminiLink gpt-image-2")
	}
	apiKey := os.Getenv("OMINILINK_API_KEY")
	baseURL := os.Getenv("OMINILINK_BASE_URL")
	if apiKey == "" || baseURL == "" {
		t.Fatal("OMINILINK_API_KEY and OMINILINK_BASE_URL are required for live test")
	}
	outDir := os.Getenv("WG_MODELHUB_LIVE_OUT")
	if outDir == "" {
		t.Fatal("WG_MODELHUB_LIVE_OUT is required for live test")
	}

	p, err := New("ominilink_gpt_image", apiKey, baseURL, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	ratio := "1:1"
	event, err := p.GenerateImage(ctx, models.GPTImage2, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{
					Content: &modelhubv2.ContentPart_Text{Text: "A simple solid red square centered on a white background, no text."},
				}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{
			AspectRatio: &ratio,
		}}},
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
		t.Fatalf("no image in live response: items=%d finish=%q", len(event.GetItems()), event.GetFinishReason())
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(outDir, "modelhub-gpt-image-2-ominilink.png")
	if err := os.WriteFile(outPath, image.GetData(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("saved %s mime=%s bytes=%d", outPath, image.GetMimeType(), len(image.GetData()))
}
