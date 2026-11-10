package rekognitiondetect

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rekognition"
	"github.com/aws/aws-sdk-go-v2/service/rekognition/types"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
)

type detectStub struct {
	input *rekognition.DetectLabelsInput
}

func (s *detectStub) DetectLabels(_ context.Context, input *rekognition.DetectLabelsInput, _ ...func(*rekognition.Options)) (*rekognition.DetectLabelsOutput, error) {
	s.input = input
	return &rekognition.DetectLabelsOutput{Labels: []types.Label{{Name: aws.String("Person"), Instances: []types.Instance{{
		BoundingBox: &types.BoundingBox{Left: aws.Float32(.25), Top: aws.Float32(.125), Width: aws.Float32(.5), Height: aws.Float32(.5)},
		Confidence:  aws.Float32(90),
	}}}}}, nil
}

func TestDetectUsesOriginalDimensionsAndBytes(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 80, 64))); err != nil {
		t.Fatal(err)
	}
	data := encoded.Bytes()
	stub := &detectStub{}
	p := &Provider{name: "test", client: stub}
	req := &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: data}}}}}}}}}}}
	event, err := p.Generate(context.Background(), models.RekognitionDetectLabels, req)
	if err != nil {
		t.Fatal(err)
	}
	if stub.input == nil || &stub.input.Image.Bytes[0] != &data[0] {
		t.Fatal("small image must reach AWS unchanged without pixel re-encoding")
	}
	var result struct {
		Persons []personBox `json:"persons"`
	}
	if err := json.Unmarshal([]byte(event.Items[0].GetText()), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Persons) != 1 || result.Persons[0].BBox != [4]float64{20, 8, 40, 32} {
		t.Fatalf("wrong original-image coordinates: %+v", result.Persons)
	}
	req.Input.Items[0].GetMessage().Parts[0].GetImage().Source = &modelhubv2.Media_Data{Data: []byte("not an image")}
	stub.input = nil
	if _, err := p.Generate(context.Background(), models.RekognitionDetectLabels, req); err == nil || stub.input != nil {
		t.Fatal("invalid dimensions must fail before AWS")
	}
}
