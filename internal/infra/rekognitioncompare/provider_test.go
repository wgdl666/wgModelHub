package rekognitioncompare

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rekognition"
	"github.com/aws/aws-sdk-go-v2/service/rekognition/types"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type stubCompare struct{}

func (stubCompare) CompareFaces(context.Context, *rekognition.CompareFacesInput, ...func(*rekognition.Options)) (*rekognition.CompareFacesOutput, error) {
	return &rekognition.CompareFacesOutput{FaceMatches: []types.CompareFacesMatch{{Similarity: aws.Float32(70)}}}, nil
}

func TestGenerateScalesSimilarityToUnitInterval(t *testing.T) {
	provider := &Provider{name: "rekognition", client: stubCompare{}}
	event, err := provider.Generate(context.Background(), models.RekognitionCompareFaces, &modelhubv2.GenerateRequest{
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{
				{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: []byte("a")}}}},
				{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{Source: &modelhubv2.Media_Data{Data: []byte("b")}}}},
			}}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Similarity float64 `json:"similarity"`
	}
	if err := json.Unmarshal([]byte(event.GetItems()[0].GetText()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Similarity < 0.699 || decoded.Similarity > 0.701 {
		t.Fatalf("similarity=%v", decoded.Similarity)
	}
}
