// Package facebodylibrary 把阿里云人脸库的查重、录脸、删脸收成文本结果。
// 库名在供应商配置。请求只带 op，录脸失败会删掉刚建的空实体。
package facebodylibrary

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

type libraryAPI interface {
	SearchFaceAdvance(*facebody.SearchFaceAdvanceRequest, *util.RuntimeOptions) (*facebody.SearchFaceResponse, error)
	AddFaceEntity(*facebody.AddFaceEntityRequest) (*facebody.AddFaceEntityResponse, error)
	AddFaceAdvance(*facebody.AddFaceAdvanceRequest, *util.RuntimeOptions) (*facebody.AddFaceResponse, error)
	DeleteFaceEntity(*facebody.DeleteFaceEntityRequest) (*facebody.DeleteFaceEntityResponse, error)
}

type Provider struct {
	name     string
	database string
	client   libraryAPI
}

func New(name, endpoint, accessKey, secret, database string) (*Provider, error) {
	name = strings.TrimSpace(name)
	endpoint = strings.TrimSpace(endpoint)
	accessKey = strings.TrimSpace(accessKey)
	secret = strings.TrimSpace(secret)
	database = strings.TrimSpace(database)
	if name == "" || endpoint == "" || accessKey == "" || secret == "" || database == "" {
		return nil, provider.New(provider.ErrorConfiguration, "facebody library requires endpoint, key pair and database")
	}
	client, err := facebody.NewClient(&openapi.Config{
		AccessKeyId: tea.String(accessKey), AccessKeySecret: tea.String(secret), Endpoint: tea.String(endpoint),
	})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorConfiguration, "create facebody client", err)
	}
	return &Provider{name: name, database: database, client: client}, nil
}

type opRequest struct {
	Op       string `json:"op"`
	EntityID string `json:"entity_id"`
	Limit    int    `json:"limit"`
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if model != models.FacebodyFaceLibrary {
		return nil, provider.Errorf(provider.ErrorInvalidArgument, "facebody library does not serve model %q", model)
	}
	if err := ctx.Err(); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" library canceled", err)
	}
	var op opRequest
	if err := json.Unmarshal([]byte(provider.UserText(inputOf(request))), &op); err != nil {
		return nil, provider.New(provider.ErrorInvalidArgument, "face library requires JSON op")
	}
	switch op.Op {
	case "search":
		return p.search(request, op.Limit)
	case "register":
		return p.register(request, op.EntityID)
	case "delete":
		return p.delete(op.EntityID)
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

func (p *Provider) search(request *modelhubv2.GenerateRequest, limit int) (*modelhubv2.GenerateEvent, error) {
	image, err := oneImage(request)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1
	}
	runtime := &util.RuntimeOptions{}
	runtime.SetAutoretry(false)
	runtime.SetMaxAttempts(1)
	response, err := p.client.SearchFaceAdvance(&facebody.SearchFaceAdvanceRequest{
		DbName: tea.String(p.database), ImageUrlObject: bytes.NewReader(image), Limit: tea.Int32(int32(limit)), MaxFaceNum: tea.Int64(1),
	}, runtime)
	if err != nil {
		if strings.Contains(err.Error(), "InvalidImage.NotFoundFace") {
			return jsonEvent(map[string]any{"entity_id": "", "score": 0})
		}
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" search face", err)
	}
	entity, score := "", 0.0
	if response != nil && response.Body != nil && response.Body.Data != nil {
		for _, match := range response.Body.Data.MatchList {
			if match == nil || len(match.FaceItems) == 0 || match.FaceItems[0] == nil {
				continue
			}
			item := match.FaceItems[0]
			if id := strings.TrimSpace(tea.StringValue(item.EntityId)); id != "" {
				entity = id
				if item.Confidence != nil {
					score = float64(*item.Confidence)
				}
				break
			}
		}
	}
	return jsonEvent(map[string]any{"entity_id": entity, "score": score})
}

func (p *Provider) register(request *modelhubv2.GenerateRequest, entityID string) (*modelhubv2.GenerateEvent, error) {
	entityID = strings.TrimSpace(entityID)
	image, err := oneImage(request)
	if err != nil || entityID == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "face register requires an image and entity_id")
	}
	if _, err := p.client.AddFaceEntity(&facebody.AddFaceEntityRequest{DbName: tea.String(p.database), EntityId: tea.String(entityID)}); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" create face entity", err)
	}
	runtime := &util.RuntimeOptions{}
	runtime.SetAutoretry(false)
	runtime.SetMaxAttempts(1)
	if _, err := p.client.AddFaceAdvance(&facebody.AddFaceAdvanceRequest{
		DbName: tea.String(p.database), EntityId: tea.String(entityID), ImageUrlObject: bytes.NewReader(image),
	}, runtime); err != nil {
		_, _ = p.client.DeleteFaceEntity(&facebody.DeleteFaceEntityRequest{DbName: tea.String(p.database), EntityId: tea.String(entityID)})
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" add face image", err)
	}
	return jsonEvent(map[string]any{"ok": true})
}

func (p *Provider) delete(entityID string) (*modelhubv2.GenerateEvent, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, provider.New(provider.ErrorInvalidArgument, "face delete requires entity_id")
	}
	if _, err := p.client.DeleteFaceEntity(&facebody.DeleteFaceEntityRequest{DbName: tea.String(p.database), EntityId: tea.String(entityID)}); err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" delete face entity", err)
	}
	return jsonEvent(map[string]any{"ok": true})
}

func inputOf(request *modelhubv2.GenerateRequest) *modelhubv2.Input {
	if request == nil {
		return nil
	}
	return request.GetInput()
}

func oneImage(request *modelhubv2.GenerateRequest) ([]byte, error) {
	data, ok := provider.InlineImageBytes(provider.FirstImageMedia(inputOf(request)))
	if !ok {
		return nil, provider.New(provider.ErrorInvalidArgument, "face library image must be inline bytes")
	}
	return data, nil
}

func jsonEvent(payload any) (*modelhubv2.GenerateEvent, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "encode face library", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}
