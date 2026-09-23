// Package rekognitiondetect 用 DetectLabels 的 Person 实例框做人检。
// 返回和 human-yolo 相同的 persons JSON，衣橱不用再区分供应商坐标。
package rekognitiondetect

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/rekognition"
	"github.com/aws/aws-sdk-go-v2/service/rekognition/types"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	xdraw "golang.org/x/image/draw"
)

const (
	minConfidence = float32(55)
	maxBytes      = 5 * 1024 * 1024
	timeout       = 15 * time.Second
)

type detectAPI interface {
	DetectLabels(ctx context.Context, params *rekognition.DetectLabelsInput, optFns ...func(*rekognition.Options)) (*rekognition.DetectLabelsOutput, error)
}

// Provider 只实现 TextProvider。空钥走任务角色，静态钥必须成对出现。
type Provider struct {
	name   string
	client detectAPI
}

func New(ctx context.Context, name, region, accessKey, secret, sessionToken string) (*Provider, error) {
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
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(httpClient),
	}
	if accessKey != "" {
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secret, sessionToken)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "load rekognition config", err)
	}
	return &Provider{name: name, client: rekognition.NewFromConfig(cfg)}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.RekognitionDetectLabels {
		return nil, provider.NotAttemptedf(provider.ErrorInvalidArgument, "rekognition-detect does not serve model %q", model)
	}
	if p == nil || p.client == nil {
		return nil, provider.New(provider.ErrorConfiguration, "rekognition client is not configured")
	}
	images := provider.ImageMedias(request.GetInput())
	if len(images) != 1 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "rekognition-detect requires exactly one input image")
	}
	data, ok := images[0].Source.(*modelhubv2.Media_Data)
	if !ok || len(data.Data) == 0 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "rekognition-detect requires inline image bytes")
	}
	decoded, _, err := image.Decode(bytes.NewReader(data.Data))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "decode person-detect image", err)
	}
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	payload, err := fitBytes(data.Data)
	if err != nil {
		return nil, err
	}
	out, err := p.client.DetectLabels(ctx, &rekognition.DetectLabelsInput{
		Image:         &types.Image{Bytes: payload},
		MinConfidence: aws.Float32(minConfidence),
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" detect labels", err)
	}
	var labels []types.Label
	if out != nil {
		labels = out.Labels
	}
	body, err := json.Marshal(map[string]any{"success": true, "persons": boxes(labels, width, height)})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" encode boxes", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	text := ""
	if len(event.GetItems()) > 0 {
		text = event.GetItems()[0].GetText()
	}
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}

type personBox struct {
	BBox  [4]float64 `json:"bbox"`
	Score float64    `json:"score"`
}

func boxes(labels []types.Label, width, height int) []personBox {
	out := make([]personBox, 0, 2)
	for _, label := range labels {
		if label.Name == nil || !strings.EqualFold(*label.Name, "Person") {
			continue
		}
		for _, instance := range label.Instances {
			box, score, ok := pixelBox(instance, width, height)
			if ok {
				out = append(out, personBox{BBox: box, Score: score})
			}
		}
	}
	return out
}

func pixelBox(instance types.Instance, width, height int) ([4]float64, float64, bool) {
	bb := instance.BoundingBox
	if bb == nil || bb.Left == nil || bb.Top == nil || bb.Width == nil || bb.Height == nil {
		return [4]float64{}, 0, false
	}
	left := max(0, float64(*bb.Left)*float64(width))
	top := max(0, float64(*bb.Top)*float64(height))
	boxWidth := min(float64(width), left+float64(*bb.Width)*float64(width)) - left
	boxHeight := min(float64(height), top+float64(*bb.Height)*float64(height)) - top
	if boxWidth <= 0 || boxHeight <= 0 {
		return [4]float64{}, 0, false
	}
	score := 0.0
	if instance.Confidence != nil {
		score = float64(*instance.Confidence)
	}
	return [4]float64{left, top, boxWidth, boxHeight}, score, true
}

func fitBytes(data []byte) ([]byte, error) {
	if len(data) <= maxBytes {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, provider.WrapNotAttempted(provider.ErrorInvalidArgument, "decode oversize person-detect image", err)
	}
	current := rgba(img)
	for attempt := 0; attempt < 6; attempt++ {
		for quality := 85; quality >= 40; quality -= 15 {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, current, &jpeg.Options{Quality: quality}); err != nil {
				return nil, provider.Wrap(provider.ErrorUnavailable, "encode person-detect jpeg", err)
			}
			if buf.Len() <= maxBytes {
				return buf.Bytes(), nil
			}
		}
		current = scale(current, 0.7)
	}
	return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "person-detect image exceeds Rekognition 5MB limit")
}

func rgba(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)
	return dst
}

func scale(src image.Image, factor float64) image.Image {
	bounds := src.Bounds()
	width := max(1, int(float64(bounds.Dx())*factor))
	height := max(1, int(float64(bounds.Dy())*factor))
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Src, nil)
	return dst
}
