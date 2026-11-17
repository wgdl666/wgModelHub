// Package facesim adapts the private AWS-G6 YuNet/SFace service.
package facesim

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/vmindgateway"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

type Provider struct {
	name    string
	gateway *vmindgateway.Client
}

func New(name string, gateway *vmindgateway.Client) *Provider {
	return &Provider{name: name, gateway: gateway}
}

func (p *Provider) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {

	images := provider.ImageMedias(request.GetInput())
	if len(images) != 2 {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "facesim requires exactly two inline images")
	}
	fields := make(map[string]string, len(images))
	for i, image := range images {
		data, ok := image.Source.(*modelhubv2.Media_Data)
		if !ok || len(data.Data) == 0 {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "facesim requires inline image bytes")
		}
		field := "image_a"
		if i == 1 {
			field = "image_b"
		}
		fields[field] = base64.StdEncoding.EncodeToString(data.Data)
	}
	response, err := p.gateway.Invoke(ctx, model, fields, 60*time.Second)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, p.name+" facesim request failed", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "read facesim response", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, provider.FromHTTPDetail(p.name, response.StatusCode, string(raw))
	}
	var result struct {
		Success    bool     `json:"success"`
		Similarity *float64 `json:"similarity"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil || !result.Success || result.Similarity == nil {
		return nil, provider.New(provider.ErrorInvalidResponse, "facesim returned an invalid or unsuccessful result")
	}
	cosine := *result.Similarity
	if math.IsNaN(cosine) || math.IsInf(cosine, 0) || cosine < -1 || cosine > 1 {
		return nil, provider.New(provider.ErrorInvalidResponse, "facesim returned an invalid cosine similarity")
	}
	// Preserve SFace's signed cosine separately. Existing face_compare callers use 0..1.
	normalized := math.Max(0, cosine)
	output, err := json.Marshal(map[string]float64{"similarity": normalized, "cosine_similarity": cosine})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "encode facesim response", err)
	}
	return provider.TextFinalEvent(string(output), nil, "", "", nil), nil
}

func (p *Provider) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	event, err := p.Generate(ctx, model, request)
	if err != nil {
		return nil, err
	}
	if emit != nil {
		if err := emit(provider.TextDeltaEvent(event.GetItems()[0].GetText())); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}
