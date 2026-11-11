package ominilinkvideo

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
)

func (p *Provider) buildVeoRequest(ctx context.Context, model, prompt, resolution string, duration int, aspectRatio string, image *modelhubv2.Media) (string, *httpbody.Body, error) {
	imageB64, mimeType, err := p.veoImageBase64(ctx, image)
	if err != nil {
		return "", nil, err
	}
	body, err := httpbody.JSON(map[string]any{
		"instances": []map[string]any{{
			"prompt": prompt,
			"image":  map[string]string{"bytesBase64Encoded": imageB64, "mimeType": mimeType},
		}},
		"parameters": map[string]any{
			"durationSeconds":  duration,
			"sampleCount":      1,
			"resolution":       veoResolutionForAPI(resolution),
			"aspectRatio":      veoAspectRatio(aspectRatio),
			"personGeneration": "allow_adult",
			"generateAudio":    false,
		},
	}, true)
	return p.baseURL + "/" + url.PathEscape(model), body, err
}

func (p *Provider) veoImageBase64(ctx context.Context, media *modelhubv2.Media) (b64, mimeType string, err error) {
	if _, ok := media.GetSource().(*modelhubv2.Media_Data); ok {
		return inlineImageBase64(media)
	}
	imageURL, err := provider.ResolveImageURL(media)
	if err != nil {
		return "", "", err
	}
	if strings.HasPrefix(imageURL, "data:") {
		// ParseDataURI 也可能解析供应商回传；此处是提交前输入，失败标本地。
		mimeType, data, err := provider.ParseDataURI(imageURL)
		if err != nil {
			return "", "", provider.AsNotAttempted(err)
		}
		return base64.StdEncoding.EncodeToString(data), mimeType, nil
	}
	return p.fetchImageBase64(ctx, imageURL)
}

func (p *Provider) fetchImageBase64(ctx context.Context, imageURL string) (b64, mimeType string, err error) {
	// 拉取输入图失败不得记成模型调用。
	data, err := provider.DownloadPublicURL(ctx, p.client, p.name, imageURL, protocol.MaxMediaBytes)
	if err != nil {
		return "", "", provider.AsNotAttempted(err)
	}
	mimeType = mimeFromURL(imageURL)
	return base64.StdEncoding.EncodeToString(data), mimeType, nil
}

func mimeFromURL(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, ".png"):
		return "image/png"
	case strings.Contains(lower, ".webp"):
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

func parseVeoSubmit(envelope map[string]any) (string, error) {
	return responseTaskID(envelope), nil
}

func parseVeoTask(envelope map[string]any) taskPollResult {
	task := taskObject(envelope)
	response, _ := envelope["response"].(map[string]any)
	if response == nil {
		response, _ = task["response"].(map[string]any)
	}
	videoURL := firstNonEmpty(directVideoURL(task), directVideoURL(envelope), directVideoURL(response), legacyResultURL(task), legacyResultURL(envelope), veoVideoURL(response), veoVideoURL(task), veoVideoURL(envelope))
	status := taskStatus(task, envelope)
	if status == "" {
		if done, ok := envelope["done"].(bool); ok {
			status = "processing"
			if done && videoURL != "" {
				status = "success"
			}
		} else if detail, ok := envelope["error"].(map[string]any); ok && len(detail) > 0 {
			status = "failed"
		}
	}
	return taskPollResult{
		state:        normalizeTaskState(status),
		videoURL:     normalizeVideoURL(videoURL),
		errorCode:    taskErrorCode(task, envelope),
		errorMessage: firstNonEmpty(taskErrorMessage(task, envelope), status),
	}
}

func veoVideoURL(object map[string]any) string {
	videos, _ := object["videos"].([]any)
	for _, video := range videos {
		item, _ := video.(map[string]any)
		if uri := stringField(item, "gcsUri"); uri != "" {
			return uri
		}
	}
	return ""
}
