package facebodycompare

import (
	"context"
	"encoding/json"
	"testing"

	facebody "github.com/alibabacloud-go/facebody-20191230/v4/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type stubCompare struct {
	confidence float32
}

func (s stubCompare) CompareFaceAdvance(*facebody.CompareFaceAdvanceRequest, *util.RuntimeOptions) (*facebody.CompareFaceResponse, error) {
	return &facebody.CompareFaceResponse{Body: &facebody.CompareFaceResponseBody{
		Data: &facebody.CompareFaceResponseBodyData{Confidence: tea.Float32(s.confidence)},
	}}, nil
}

func TestGenerateNormalizesConfidence(t *testing.T) {
	provider := &Provider{name: "facebody", client: stubCompare{confidence: 82}}
	event, err := provider.Generate(context.Background(), models.FacebodyCompareFace, twoImageRequest())
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Similarity float64 `json:"similarity"`
	}
	if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Similarity < 0.819 || decoded.Similarity > 0.821 {
		t.Fatalf("similarity=%v", decoded.Similarity)
	}
}

func twoImageRequest() *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
		Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{
			{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: []byte("a")}}}},
			{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: []byte("b")}}}},
		}}},
	}}}}
}
