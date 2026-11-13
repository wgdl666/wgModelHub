package ominilinkvideo

import (
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func (p *Provider) buildSeedanceRequest(model, prompt, resolution string, duration int, aspectRatio string, images []*modelhubv2.Media) (string, *httpbody.Body, error) {
	// 单图是首帧，多图是身份参考；直接构造最终 payload，每张图只转换一次。
	role, generateAudio := "first_frame", true
	if len(images) > 1 {
		role, generateAudio = "reference_image", false
	}
	content := []map[string]any{{"type": "text", "text": prompt}}
	for _, image := range images {
		imageURL, err := provider.ResolveImageURL(image)
		if err != nil {
			return "", nil, err
		}
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}, "role": role})
	}
	body, err := httpbody.JSON(map[string]any{
		"model":             model,
		"content":           content,
		"resolution":        resolution,
		"ratio":             aspectRatio,
		"duration":          duration,
		"generate_audio":    generateAudio,
		"watermark":         false,
		"return_last_frame": false,
	}, true)
	return p.seedanceCreateURL(), body, err
}

// Seedance 使用独立 v3 路由，其余 family 仍使用 v1。
func (p *Provider) seedanceCreateURL() string {
	return strings.TrimSuffix(p.baseURL, "/v1") + "/v3/bytedance/api/v3/contents/generations/tasks"
}

func parseSeedanceSubmit(envelope map[string]any) (string, error) {
	return responseTaskID(envelope), nil
}

func parseSeedanceTask(envelope map[string]any) taskPollResult {
	task := taskObject(envelope)
	status := taskStatus(task, envelope)
	content, _ := task["content"].(map[string]any)
	rootContent, _ := envelope["content"].(map[string]any)
	return taskPollResult{
		state:        normalizeTaskState(status),
		videoURL:     normalizeVideoURL(firstNonEmpty(directVideoURL(task), directVideoURL(envelope), directVideoURL(content), directVideoURL(rootContent), legacyResultURL(task), legacyResultURL(envelope))),
		errorCode:    taskErrorCode(task, envelope),
		errorMessage: firstNonEmpty(taskErrorMessage(task, envelope), status),
	}
}
