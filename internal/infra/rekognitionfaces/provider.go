// Package rekognitionfaces 把 DetectFaces 和 Collection 人脸库收成文本结果。
// 两个模型分两个 provider 实例，不能和 DetectLabels 人检共用。
package rekognitionfaces

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rekognition"
	"github.com/aws/aws-sdk-go-v2/service/rekognition/types"
	"github.com/aws/smithy-go"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	maxBytes = 5 * 1024 * 1024
	timeout  = 15 * time.Second
)

type faceAPI interface {
	DetectFaces(ctx context.Context, params *rekognition.DetectFacesInput, optFns ...func(*rekognition.Options)) (*rekognition.DetectFacesOutput, error)
	SearchFacesByImage(ctx context.Context, params *rekognition.SearchFacesByImageInput, optFns ...func(*rekognition.Options)) (*rekognition.SearchFacesByImageOutput, error)
	IndexFaces(ctx context.Context, params *rekognition.IndexFacesInput, optFns ...func(*rekognition.Options)) (*rekognition.IndexFacesOutput, error)
	ListFaces(ctx context.Context, params *rekognition.ListFacesInput, optFns ...func(*rekognition.Options)) (*rekognition.ListFacesOutput, error)
	DeleteFaces(ctx context.Context, params *rekognition.DeleteFacesInput, optFns ...func(*rekognition.Options)) (*rekognition.DeleteFacesOutput, error)
}

// Provider 由 mode 决定只服务检脸或只服务人脸库。
type Provider struct {
	name       string
	collection string
	detect     bool
	client     faceAPI
}

func NewDetect(ctx context.Context, name, region, accessKey, secret, sessionToken string) (*Provider, error) {
	client, err := newClient(ctx, name, region, accessKey, secret, sessionToken)
	if err != nil {
		return nil, err
	}
	return &Provider{name: name, detect: true, client: client}, nil
}

func NewLibrary(ctx context.Context, name, region, accessKey, secret, sessionToken, collection string) (*Provider, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return nil, provider.New(provider.ErrorConfiguration, "rekognition face library collection is required")
	}
	client, err := newClient(ctx, name, region, accessKey, secret, sessionToken)
	if err != nil {
		return nil, err
	}
	return &Provider{name: name, collection: collection, client: client}, nil
}

func newClient(ctx context.Context, name, region, accessKey, secret, sessionToken string) (*rekognition.Client, error) {
	name = strings.TrimSpace(name)
	region = strings.TrimSpace(region)
	accessKey = strings.TrimSpace(accessKey)
	secret = strings.TrimSpace(secret)
	sessionToken = strings.TrimSpace(sessionToken)
	if name == "" || region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
		return nil, provider.New(provider.ErrorConfiguration, "rekognition region is required and cannot be cn-*")
	}
	if (accessKey == "") != (secret == "") || (sessionToken != "" && accessKey == "") {
		return nil, provider.New(provider.ErrorConfiguration, "rekognition access key and secret must be configured together")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpClient := telemetry.NewHTTPClient()
	httpClient.Timeout = timeout
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region), awsconfig.WithHTTPClient(httpClient)}
	if accessKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secret, sessionToken)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "load rekognition config", err)
	}
	return rekognition.NewFromConfig(cfg), nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if p.detect {
		if model != models.RekognitionDetectFaces {
			return nil, provider.Errorf(provider.ErrorInvalidArgument, "rekognition detect faces does not serve model %q", model)
		}
		return p.detectFaces(ctx, request)
	}
	if model != models.RekognitionFaceLibrary {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "rekognition face library does not serve model %q", model)
	}
	var op struct {
		Op       string `json:"op"`
		EntityID string `json:"entity_id"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal([]byte(provider.UserText(inputOf(request))), &op); err != nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "face library requires JSON op")
	}
	switch op.Op {
	case "search":
		return p.search(ctx, request, op.Limit)
	case "register":
		return p.register(ctx, request, op.EntityID)
	case "delete":
		return p.delete(ctx, op.EntityID)
	default:
		return nil, provider.New(provider.ErrorInvalidArgument, "face library op must be search, register or delete")
	}
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	text := event.GetItems()[0].GetText()
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

func (p *Provider) detectFaces(ctx context.Context, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	data, err := fittedImage(request)
	if err != nil {
		return nil, err
	}
	out, err := p.client.DetectFaces(ctx, &rekognition.DetectFacesInput{Image: &types.Image{Bytes: data}})
	if err != nil {
		if isNoFace(err) {
			return jsonEvent(map[string]any{"faces": []any{}})
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" detect faces", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, "decode detect image", err)
	}
	bounds := img.Bounds()
	limit := maxFacesFrom(request)
	faces := make([]map[string]int, 0)
	if out != nil {
		for _, detail := range out.FaceDetails {
			if detail.BoundingBox == nil {
				continue
			}
			box := detail.BoundingBox
			item := map[string]int{
				"left": int(float64(bounds.Dx()) * float64(awsFloat(box.Left))), "top": int(float64(bounds.Dy()) * float64(awsFloat(box.Top))),
				"width": int(float64(bounds.Dx()) * float64(awsFloat(box.Width))), "height": int(float64(bounds.Dy()) * float64(awsFloat(box.Height))),
			}
			if item["width"] <= 0 || item["height"] <= 0 {
				continue
			}
			faces = append(faces, item)
			if len(faces) >= limit {
				break
			}
		}
	}
	return jsonEvent(map[string]any{"faces": faces})
}

func (p *Provider) search(ctx context.Context, request *modelhubv2.GenerateRequest, limit int) (*modelhubv2.GenerateEvent, error) {
	data, err := fittedImage(request)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1
	}
	threshold := float32(0)
	maxFaces := int32(limit)
	out, err := p.client.SearchFacesByImage(ctx, &rekognition.SearchFacesByImageInput{
		CollectionId: &p.collection, Image: &types.Image{Bytes: data}, FaceMatchThreshold: &threshold, MaxFaces: &maxFaces,
	})
	if err != nil {
		if isNoFace(err) {
			return jsonEvent(map[string]any{"entity_id": "", "score": 0})
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" search faces", err)
	}
	entity, score := "", 0.0
	if out != nil && len(out.FaceMatches) > 0 && out.FaceMatches[0].Face != nil {
		entity = strings.TrimSpace(awsString(out.FaceMatches[0].Face.ExternalImageId))
		if out.FaceMatches[0].Similarity != nil {
			score = float64(*out.FaceMatches[0].Similarity)
		}
	}
	return jsonEvent(map[string]any{"entity_id": entity, "score": score})
}

func (p *Provider) register(ctx context.Context, request *modelhubv2.GenerateRequest, entityID string) (*modelhubv2.GenerateEvent, error) {
	entityID = strings.TrimSpace(entityID)
	data, err := fittedImage(request)
	if err != nil || entityID == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "face register requires an image and entity_id")
	}
	maxFaces := int32(1)
	if _, err := p.client.IndexFaces(ctx, &rekognition.IndexFacesInput{
		CollectionId: &p.collection, Image: &types.Image{Bytes: data}, ExternalImageId: &entityID, MaxFaces: &maxFaces, QualityFilter: types.QualityFilterNone,
	}); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" index face", err)
	}
	return jsonEvent(map[string]any{"ok": true})
}

func (p *Provider) delete(ctx context.Context, entityID string) (*modelhubv2.GenerateEvent, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "face delete requires entity_id")
	}
	var faceIDs []string
	var token *string
	for {
		out, err := p.client.ListFaces(ctx, &rekognition.ListFacesInput{CollectionId: &p.collection, MaxResults: awsInt32(100), NextToken: token})
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" list faces", err)
		}
		if out == nil {
			break
		}
		for _, item := range out.Faces {
			if strings.TrimSpace(awsString(item.ExternalImageId)) == entityID && item.FaceId != nil {
				faceIDs = append(faceIDs, *item.FaceId)
			}
		}
		if out.NextToken == nil || strings.TrimSpace(*out.NextToken) == "" {
			break
		}
		token = out.NextToken
	}
	if len(faceIDs) > 0 {
		if _, err := p.client.DeleteFaces(ctx, &rekognition.DeleteFacesInput{CollectionId: &p.collection, FaceIds: faceIDs}); err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" delete faces", err)
		}
	}
	return jsonEvent(map[string]any{"ok": true})
}

func fittedImage(request *modelhubv2.GenerateRequest) ([]byte, error) {
	data, ok := provider.InlineImageBytes(provider.FirstImageMedia(inputOf(request)))
	if !ok {
		return nil, provider.New(provider.ErrorInvalidArgument, "rekognition face requires one inline image")
	}
	if len(data) > maxBytes {
		return nil, provider.New(provider.ErrorInvalidArgument, "rekognition image exceeds 5MB")
	}
	return data, nil
}

func isNoFace(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "InvalidParameterException" && strings.Contains(strings.ToLower(apiErr.ErrorMessage()), "no faces")
}

func maxFacesFrom(request *modelhubv2.GenerateRequest) int {
	var parsed struct {
		MaxFaces int `json:"max_faces"`
	}
	if err := json.Unmarshal([]byte(provider.UserText(inputOf(request))), &parsed); err != nil || parsed.MaxFaces <= 0 {
		return 10
	}
	if parsed.MaxFaces > 30 {
		return 30
	}
	return parsed.MaxFaces
}

func inputOf(request *modelhubv2.GenerateRequest) *modelhubv2.Input {
	if request == nil {
		return nil
	}
	return request.GetInput()
}

func jsonEvent(payload any) (*modelhubv2.GenerateEvent, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "encode rekognition face", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func awsFloat(v *float32) float32 {
	if v == nil {
		return 0
	}
	return *v
}

func awsString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func awsInt32(v int32) *int32 { return &v }
