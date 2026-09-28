// Package imageseg 将 Muse 既有阿里云抠图模型迁入网关；图像缩放/裁剪仍由业务调用方负责。
package imageseg

import (
	"bytes"
	"context"
	"encoding/json"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	seg "github.com/alibabacloud-go/imageseg-20191230/v2/client"
	util "github.com/alibabacloud-go/tea-utils/v2/service"
	"github.com/alibabacloud-go/tea/tea"
	pb "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/telemetry"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"io"
	"net/http"
)

type segmentAPI interface {
	SegmentBodyAdvance(*seg.SegmentBodyAdvanceRequest, *util.RuntimeOptions) (*seg.SegmentBodyResponse, error)
	SegmentHDBodyAdvance(*seg.SegmentHDBodyAdvanceRequest, *util.RuntimeOptions) (*seg.SegmentHDBodyResponse, error)
	SegmentCommodityAdvance(*seg.SegmentCommodityAdvanceRequest, *util.RuntimeOptions) (*seg.SegmentCommodityResponse, error)
	SegmentCommonImageAdvance(*seg.SegmentCommonImageAdvanceRequest, *util.RuntimeOptions) (*seg.SegmentCommonImageResponse, error)
}
type Provider struct {
	client segmentAPI
	http   *http.Client
}

func New(endpoint, key, secret string) (*Provider, error) {
	c, err := seg.NewClient(&openapi.Config{Endpoint: tea.String(endpoint), AccessKeyId: tea.String(key), AccessKeySecret: tea.String(secret), RegionId: tea.String("cn-shanghai")})
	if err != nil {
		return nil, err
	}
	return &Provider{c, telemetry.NewHTTPClient()}, nil
}
func (p *Provider) GenerateImage(ctx context.Context, model string, req *pb.GenerateRequest) (*pb.GenerateEvent, error) {
	data, ok := provider.InlineImageBytes(provider.FirstImageMedia(req.GetInput()))
	if !ok {
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "imageseg requires inline image")
	}
	var opts struct {
		ReturnForm string `json:"return_form"`
	}
	if text := provider.UserText(req.GetInput()); text != "" {
		if err := json.Unmarshal([]byte(text), &opts); err != nil {
			return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "imageseg requires JSON options")
		}
	}
	if opts.ReturnForm == "" {
		opts.ReturnForm = "crop"
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime := &util.RuntimeOptions{ConnectTimeout: tea.Int(5000), ReadTimeout: tea.Int(120000), Autoretry: tea.Bool(false)}
	var uri string
	switch model {
	case "aliyun-segment-body":
		r, err := p.client.SegmentBodyAdvance(&seg.SegmentBodyAdvanceRequest{ImageURLObject: bytes.NewReader(data), ReturnForm: tea.String(opts.ReturnForm)}, runtime)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, "segment body", err)
		}
		if r.Body != nil && r.Body.Data != nil {
			uri = tea.StringValue(r.Body.Data.ImageURL)
		}
	case "aliyun-segment-hd-body":
		r, err := p.client.SegmentHDBodyAdvance(&seg.SegmentHDBodyAdvanceRequest{ImageURLObject: bytes.NewReader(data)}, runtime)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, "segment HD body", err)
		}
		if r.Body != nil && r.Body.Data != nil {
			uri = tea.StringValue(r.Body.Data.ImageURL)
		}
	case "aliyun-segment-commodity":
		r, err := p.client.SegmentCommodityAdvance(&seg.SegmentCommodityAdvanceRequest{ImageURLObject: bytes.NewReader(data), ReturnForm: tea.String(opts.ReturnForm)}, runtime)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, "segment commodity", err)
		}
		if r.Body != nil && r.Body.Data != nil {
			uri = tea.StringValue(r.Body.Data.ImageURL)
		}
	case "aliyun-segment-common":
		r, err := p.client.SegmentCommonImageAdvance(&seg.SegmentCommonImageAdvanceRequest{ImageURLObject: bytes.NewReader(data), ReturnForm: tea.String(opts.ReturnForm)}, runtime)
		if err != nil {
			return nil, provider.Wrap(provider.ErrorUnavailable, "segment common", err)
		}
		if r.Body != nil && r.Body.Data != nil {
			uri = tea.StringValue(r.Body.Data.ImageURL)
		}
	default:
		return nil, provider.NotAttempted(provider.ErrorInvalidArgument, "unknown imageseg model")
	}
	if uri == "" {
		return nil, provider.New(provider.ErrorInvalidResponse, "imageseg returned no image URL")
	}
	download, err := http.NewRequestWithContext(ctx, "GET", uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.http.Do(download)
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "download segment image", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, provider.Errorf(provider.ErrorUnavailable, "segment download HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 60<<20))
	if err != nil {
		return nil, err
	}
	return &pb.GenerateEvent{Final: true, Items: []*pb.OutputItem{{Item: &pb.OutputItem_Image{Image: &pb.Media{MimeType: "image/png", Source: &pb.Media_Data{Data: body}}}}}}, nil
}
