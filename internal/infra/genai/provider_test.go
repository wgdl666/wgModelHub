package genai

import (
	"encoding/json"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	genaisdk "google.golang.org/genai"
)

func TestBuildContentsPreservesThoughtSignatureRoundtrip(t *testing.T) {
	signature := []byte("thought-sig-abc")
	request := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_ASSISTANT,
					ToolCalls: []*modelhubv2.ToolCall{{
						Id:               "call-1",
						Name:             "lookup",
						ArgumentsJson:    []byte(`{"q":"red dress"}`),
						ThoughtSignature: signature,
					}},
				}},
			}},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	}
	p := &Provider{}
	contents := p.buildContents(request)
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
	request := &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{{
				Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role: modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{
						{Content: &modelhubv2.ContentPart_Text{Text: "stylize"}},
						{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
							MimeType: "image/png",
							Source:   &modelhubv2.Media_Data{Data: []byte("body")},
						}}},
						{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{
							MimeType: "image/jpeg",
							Source:   &modelhubv2.Media_Uri{Uri: "https://cdn.example/cloth.jpg"},
						}}},
						{Content: &modelhubv2.ContentPart_Text{Text: "final"}},
					},
				}},
			}},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}},
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

func TestBuildContentsKeepsMessageAndToolOutputOrder(t *testing.T) {
	p := &Provider{}
	contents := p.buildContents(&modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "ask"}}},
				}}},
				{Item: &modelhubv2.InputItem_ToolOutput{ToolOutput: &modelhubv2.ToolOutput{
					ToolCallId: "c1",
					ToolName:   "lookup",
					Output:     `{"ok":true}`,
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "followup"}}},
				}}},
			},
		},
	})
	if len(contents) != 3 {
		t.Fatalf("contents len = %d", len(contents))
	}
	if contents[0].Parts[0].Text != "ask" {
		t.Fatalf("first message lost: %#v", contents[0])
	}
	if contents[1].Parts[0].FunctionResponse == nil {
		t.Fatalf("tool output missing: %#v", contents[1])
	}
	if contents[2].Parts[0].Text != "followup" {
		t.Fatalf("followup message lost: %#v", contents[2])
	}
}

func TestConvertMessageKeepsTextWithToolCalls(t *testing.T) {
	content := convertMessage(&modelhubv2.Message{
		Role:  modelhubv2.Role_ROLE_ASSISTANT,
		Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "calling tool"}}},
		ToolCalls: []*modelhubv2.ToolCall{{
			Id:            "call-1",
			Name:          "lookup",
			ArgumentsJson: []byte(`{"q":"x"}`),
		}},
	})
	if content == nil || len(content.Parts) != 2 {
		t.Fatalf("content = %#v", content)
	}
	if content.Parts[0].Text != "calling tool" {
		t.Fatalf("text part lost: %#v", content.Parts[0])
	}
	if content.Parts[1].FunctionCall == nil || content.Parts[1].FunctionCall.Name != "lookup" {
		t.Fatalf("tool call lost: %#v", content.Parts[1])
	}
}

func TestBuildConfigJoinsLeadingSystemTexts(t *testing.T) {
	cfg := (&Provider{}).buildConfig(&modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{
			Items: []*modelhubv2.InputItem{
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "persona"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "rules"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_USER,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "hi"}}},
				}}},
				{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
					Role:  modelhubv2.Role_ROLE_SYSTEM,
					Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: "mid"}}},
				}}},
			},
		},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Text{Text: &modelhubv2.TextOutput{}}},
	})
	if cfg.SystemInstruction == nil || len(cfg.SystemInstruction.Parts) != 1 {
		t.Fatalf("system instruction = %#v", cfg.SystemInstruction)
	}
	if got := cfg.SystemInstruction.Parts[0].Text; got != "persona\nrules" {
		t.Fatalf("system text = %q", got)
	}
}

func TestBuildImageConfigPreservesZeroTemperature(t *testing.T) {
	zero := 0.0
	ratio := "3:4"
	size := "2K"
	cfg := buildImageConfig(&modelhubv2.GenerateRequest{
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{
			Temperature:   &zero,
			AspectRatio:   &ratio,
			ImageSize:     &size,
			ThinkingLevel: modelhubv2.ThinkingLevel_THINKING_LEVEL_LOW,
			OutputModalities: []modelhubv2.ImageOutputModality{
				modelhubv2.ImageOutputModality_IMAGE_OUTPUT_MODALITY_IMAGE,
				modelhubv2.ImageOutputModality_IMAGE_OUTPUT_MODALITY_TEXT,
			},
		}}},
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
