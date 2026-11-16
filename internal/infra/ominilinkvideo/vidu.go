package ominilinkvideo

import (
	"net/url"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func (p *Provider) buildViduRequest(model, prompt, resolution string, duration int, image *modelhubv2.Media) (string, *httpbody.Body, error) {
	imageURL, err := provider.ResolveImageURL(image)
	if err != nil {
		return "", nil, err
	}
	body, err := httpbody.JSON(map[string]any{
		"model":      model,
		"images":     []string{imageURL},
		"prompt":     prompt,
		"duration":   duration,
		"resolution": resolution,
		"audio":      false,
	}, true)
	return p.baseURL + "/" + url.PathEscape(model) + "/img2video", body, err
}

func parseViduSubmit(envelope map[string]any) (string, error) {
	return responseTaskID(envelope), nil
}

func parseViduTask(envelope map[string]any) taskPollResult {
	task := taskObject(envelope)
	videoURL := firstNonEmpty(directVideoURL(task), directVideoURL(envelope), legacyResultURL(task), legacyResultURL(envelope), viduCreationURL(task), viduCreationURL(envelope))
	status := taskStatus(task, envelope)
	return taskPollResult{
		state:        normalizeTaskState(status),
		videoURL:     normalizeVideoURL(videoURL),
		errorCode:    firstNonEmpty(stringField(task, "err_code"), taskErrorCode(task, envelope)),
		errorMessage: firstNonEmpty(stringField(task, "err_msg"), taskErrorMessage(task, envelope), status),
	}
}

func viduCreationURL(object map[string]any) string {
	creations, _ := object["creations"].([]any)
	for _, creation := range creations {
		item, _ := creation.(map[string]any)
		if videoURL := stringField(item, "url"); videoURL != "" {
			return videoURL
		}
	}
	return ""
}
