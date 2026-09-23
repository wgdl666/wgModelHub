// Package facebodydetect 把阿里云 DetectFace 收成脸框 JSON。
// 扩展裁图是本地几何，留在调用方，不在这里编码成图片。
package facebodydetect

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	facebody "github.com/alibabacloud-go/facebody-20191230/v4/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	connectTimeoutMS = 5_000
	readTimeoutMS    = 15_000
)

type detectAPI interface {
	DetectFaceAdvance(*facebody.DetectFaceAdvanceRequest, *util.RuntimeOptions) (*facebody.DetectFaceResponse, error)
}

type Provider struct {
	name   string
	client detectAPI
}

func New(name, endpoint, accessKey, secret string) (*Provider, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimSpace(endpoint)
	accessKey = strings.TrimSpace(accessKey)
	secret = strings.TrimSpace(secret)
	if name == "" || endpoint == "" || accessKey == "" || secret == "" {
		return nil, provider.New(provider.ErrorConfiguration, "facebody detect requires endpoint and a key pair")
	}
	client, err := facebody.NewClient(&openapi.Config{
		AccessKeyId: tea.String(accessKey), AccessKeySecret: tea.String(secret), Endpoint: tea.String(endpoint),
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "create facebody client", err)
	}
	return &Provider{name: name, client: client}, nil
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.FacebodyDetectFace {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "facebody detect does not serve model %q", model)
	}
	if err := ctx.Err(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" detect canceled", err)
	}
	image, err := oneImage(request)
	if err != nil {
		return nil, err
	}
	runtime := &util.RuntimeOptions{}
	runtime.SetConnectTimeout(connectTimeoutMS)
	runtime.SetReadTimeout(readTimeoutMS)
	runtime.SetAutoretry(false)
	runtime.SetMaxAttempts(1)
	maxFaces := int64(maxFacesFrom(request))
	response, err := p.client.DetectFaceAdvance(&facebody.DetectFaceAdvanceRequest{
		ImageURLObject: bytes.NewReader(image),
		MaxFaceNumber:  tea.Int64(maxFaces),
		Landmark:       tea.Bool(false),
		Pose:           tea.Bool(false),
		Quality:        tea.Bool(false),
	}, runtime)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidImage.NotFoundFace") {
			return facesEvent(nil)
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" detect face", err)
	}
	var rectangles []*int32
	if response != nil && response.Body != nil && response.Body.Data != nil {
		rectangles = response.Body.Data.FaceRectangles
	}
	return facesEvent(boxes(rectangles, maxFaces))
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

type box struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func boxes(values []*int32, limit int64) []box {
	out := make([]box, 0)
	for index := 0; index+3 < len(values); index += 4 {
		item := box{Left: int(tea.Int32Value(values[index])), Top: int(tea.Int32Value(values[index+1])), Width: int(tea.Int32Value(values[index+2])), Height: int(tea.Int32Value(values[index+3]))}
		if item.Width <= 0 || item.Height <= 0 {
			continue
		}
		out = append(out, item)
		if limit > 0 && int64(len(out)) >= limit {
			break
		}
	}
	return out
}

func facesEvent(items []box) (*modelhubv2.GenerateEvent, error) {
	if items == nil {
		items = []box{}
	}
	body, err := json.Marshal(map[string]any{"faces": items})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "encode face boxes", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func oneImage(request *modelhubv2.GenerateRequest) ([]byte, error) {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	data, ok := provider.InlineImageBytes(provider.FirstImageMedia(input))
	if !ok {
		return nil, provider.New(provider.ErrorInvalidArgument, "face detect requires one inline image")
	}
	return data, nil
}

func maxFacesFrom(request *modelhubv2.GenerateRequest) int {
	var input *modelhubv2.Input
	if request != nil {
		input = request.GetInput()
	}
	var parsed struct {
		MaxFaces int `json:"max_faces"`
	}
	if err := json.Unmarshal([]byte(provider.UserText(input)), &parsed); err != nil || parsed.MaxFaces <= 0 {
		return 10
	}
	if parsed.MaxFaces > 30 {
		return 30
	}
	return parsed.MaxFaces
}
