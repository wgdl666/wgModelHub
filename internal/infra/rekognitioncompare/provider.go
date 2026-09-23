// Package rekognitioncompare 把 Rekognition CompareFaces 收成 ModelHub 文本结果。
// 与 DetectLabels 人检分实例：那个模型返回人体框，这个模型只返回相似度。
package rekognitioncompare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
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
	xdraw "golang.org/x/image/draw"
)

const (
	maxBytes = 5 * 1024 * 1024
	timeout  = 15 * time.Second
)

type compareAPI interface {
	CompareFaces(ctx context.Context, params *rekognition.CompareFacesInput, optFns ...func(*rekognition.Options)) (*rekognition.CompareFacesOutput, error)
}

// Provider 只实现 TextProvider。空钥走任务角色。第一张图是原图，第二张图是生成图。
type Provider struct {
	name   string
	client compareAPI
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
	if model != models.RekognitionCompareFaces {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "rekognition-compare does not serve model %q", model)
	}
	if p == nil || p.client == nil {
		return nil, provider.New(provider.ErrorConfiguration, "rekognition client is not configured")
	}
	source, generated, err := inlinePair(request)
	if err != nil {
		return nil, err
	}
	source, err = fitBytes(source)
	if err != nil {
		return nil, err
	}
	generated, err = fitBytes(generated)
	if err != nil {
		return nil, err
	}
	threshold := float32(0)
	out, err := p.client.CompareFaces(ctx, &rekognition.CompareFacesInput{
		SourceImage:         &types.Image{Bytes: source},
		TargetImage:         &types.Image{Bytes: generated},
		SimilarityThreshold: &threshold,
	})
	if err != nil {
		if isNoFace(err) {
			return similarityEvent(0)
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" compare faces", err)
	}
	best := float32(0)
	if out != nil {
		for _, match := range out.FaceMatches {
			if match.Similarity != nil && *match.Similarity > best {
				best = *match.Similarity
			}
		}
	}
	return similarityEvent(float64(best) / 100)
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

func similarityEvent(similarity float64) (*modelhubv2.GenerateEvent, error) {
	if similarity < 0 {
		similarity = 0
	}
	if similarity > 1 {
		similarity = 1
	}
	body, err := json.Marshal(map[string]float64{"similarity": similarity})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "encode similarity", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func inlinePair(request *modelhubv2.GenerateRequest) ([]byte, []byte, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	images := provider.ImageMedias(input)
	if len(images) != 2 {
		return nil, nil, provider.New(provider.ErrorInvalidArgument, "face compare requires exactly two input images")
	}
	source, ok := imageBytes(images[0])
	if !ok {
		return nil, nil, provider.New(provider.ErrorInvalidArgument, "face compare requires inline image bytes")
	}
	generated, ok := imageBytes(images[1])
	if !ok {
		return nil, nil, provider.New(provider.ErrorInvalidArgument, "face compare requires inline image bytes")
	}
	return source, generated, nil
}

func imageBytes(media *modelhubv2.Media) ([]byte, bool) {
	data, ok := media.Source.(*modelhubv2.Media_Data)
	return data.Data, ok && len(data.Data) > 0
}

func isNoFace(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "InvalidParameterException" {
		return false
	}
	return strings.Contains(strings.ToLower(apiErr.ErrorMessage()), "no faces")
}

// fitBytes 把超过 CompareFaces 5MB 上限的图压成 JPEG。调用方仍传原图，压缩只发生在出站前。
func fitBytes(data []byte) ([]byte, error) {
	if len(data) <= maxBytes {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidArgument, "decode oversize compare image", err)
	}
	current := rgbaOf(img)
	for attempt := 0; attempt < 6; attempt++ {
		for quality := 85; quality >= 40; quality -= 15 {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, current, &jpeg.Options{Quality: quality}); err != nil {
				return nil, provider.Wrap(provider.ErrorUnavailable, "encode compare jpeg", err)
			}
			if buf.Len() <= maxBytes {
				return buf.Bytes(), nil
			}
		}
		current = scale(current, 0.7)
	}
	return nil, provider.New(provider.ErrorInvalidArgument, "compare image exceeds rekognition 5MB limit")
}

func rgbaOf(img image.Image) *image.NRGBA {
	bounds := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Src)
	return dst
}

func scale(src *image.NRGBA, factor float64) *image.NRGBA {
	width := max(1, int(float64(src.Bounds().Dx())*factor))
	height := max(1, int(float64(src.Bounds().Dy())*factor))
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}
