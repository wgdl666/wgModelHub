package genai

import (
	"encoding/json"
	"testing"

	modelhubv1 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v1"
	genaisdk "google.golang.org/genai"
)

func TestBuildContentsPreservesThoughtSignatureRoundtrip(t *testing.T) {
	signature := []byte("thought-sig-abc")
	request := &modelhubv1.GenerateTextRequest{
		Messages: []*modelhubv1.Message{
			{
				Role: modelhubv1.Role_ROLE_ASSISTANT,
				ToolCalls: []*modelhubv1.ToolCall{
					{
						Id:               "call-1",
						Name:             "lookup",
						ArgumentsJson:    []byte(`{"q":"red dress"}`),
						ThoughtSignature: signature,
					},
				},
			},
		},
	}
	provider := &Provider{}
	contents := provider.buildContents(request)
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("contents = %#v", contents)
	}
	part := contents[0].Parts[0]
	if part.FunctionCall == nil || part.FunctionCall.Name != "lookup" {
		t.Fatalf("function call = %#v", part.FunctionCall)
	}
	if string(part.ThoughtSignature) != string(signature) {
		t.Fatalf("thought signature = %q", part.ThoughtSignature)
	}

	roundtrip := convertFunctionCall(part)
	if string(roundtrip.ThoughtSignature) != string(signature) {
		t.Fatalf("roundtrip signature = %q", roundtrip.ThoughtSignature)
	}
	if roundtrip.Id != "call-1" || roundtrip.Name != "lookup" {
		t.Fatalf("roundtrip call = %#v", roundtrip)
	}
	var args map[string]any
	if err := json.Unmarshal(roundtrip.ArgumentsJson, &args); err != nil || args["q"] != "red dress" {
		t.Fatalf("arguments = %s err=%v", roundtrip.ArgumentsJson, err)
	}
}

func TestBuildImagePartsPreservesOrder(t *testing.T) {
	request := &modelhubv1.GenerateImageRequest{
		Parts: []*modelhubv1.ContentPart{
			{Content: &modelhubv1.ContentPart_Text{Text: "stylize"}},
			{Content: &modelhubv1.ContentPart_Image{Image: &modelhubv1.Media{
				MimeType: "image/png",
				Source:   &modelhubv1.Media_Data{Data: []byte("body")},
			}}},
			{Content: &modelhubv1.ContentPart_Image{Image: &modelhubv1.Media{
				MimeType: "image/jpeg",
				Source:   &modelhubv1.Media_Uri{Uri: "https://cdn.example/cloth.jpg"},
			}}},
			{Content: &modelhubv1.ContentPart_Text{Text: "final"}},
		},
	}
	parts := buildImageParts(request)
	if len(parts) != 4 {
		t.Fatalf("parts len = %d", len(parts))
	}
	if parts[0].Text != "stylize" {
		t.Fatalf("part[0] = %#v", parts[0])
	}
	if parts[1].InlineData == nil || string(parts[1].InlineData.Data) != "body" {
		t.Fatalf("part[1] = %#v", parts[1])
	}
	if parts[2].FileData == nil || parts[2].FileData.FileURI != "https://cdn.example/cloth.jpg" {
		t.Fatalf("part[2] = %#v", parts[2])
	}
	if parts[3].Text != "final" {
		t.Fatalf("part[3] = %#v", parts[3])
	}
	_ = genaisdk.RoleUser
}

func TestBuildImageConfigPreservesZeroTemperature(t *testing.T) {
	zero := 0.0
	ratio := "3:4"
	size := "2K"
	cfg := buildImageConfig(&modelhubv1.GenerateImageRequest{
		Temperature:   &zero,
		AspectRatio:   &ratio,
		ImageSize:     &size,
		ThinkingLevel: modelhubv1.ThinkingLevel_THINKING_LEVEL_LOW,
		OutputModalities: []modelhubv1.ImageOutputModality{
			modelhubv1.ImageOutputModality_IMAGE_OUTPUT_MODALITY_IMAGE,
			modelhubv1.ImageOutputModality_IMAGE_OUTPUT_MODALITY_TEXT,
		},
	})
	if cfg.Temperature == nil || *cfg.Temperature != 0 {
		t.Fatalf("temperature = %#v", cfg.Temperature)
	}
	if cfg.ImageConfig == nil || cfg.ImageConfig.AspectRatio != "3:4" || cfg.ImageConfig.ImageSize != "2K" {
		t.Fatalf("image config = %#v", cfg.ImageConfig)
	}
	if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingLevel != genaisdk.ThinkingLevelLow {
		t.Fatalf("thinking = %#v", cfg.ThinkingConfig)
	}
}
