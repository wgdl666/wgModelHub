package ominilinkvideo

import (
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/httpbody"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func (p *Provider) buildKlingRequest(model, prompt, resolution string, duration int, image *modelhubv2.Media) (string, *httpbody.Body, error) {
	imageField, err := klingImageField(image)
	if err != nil {
		return "", nil, err
	}
	payload := map[string]any{
		"Image":    imageField,
		"Prompt":   prompt,
		"Duration": strconv.Itoa(duration),
		"Mode":     klingMode(resolution),
		"Sound":    "off",
		"LogoAdd":  0,
	}
	if version := klingAPIVersion(model); version != "" {
		payload["Model"] = version
	}
	body, err := httpbody.JSON(payload, true)
	return p.baseURL + "/" + url.PathEscape(model) + "/image2video", body, err
}

// klingImageField 对齐腾讯云 Image：Url 与 Base64 二选一。
func klingImageField(media *modelhubv2.Media) (map[string]string, error) {
	if _, ok := media.GetSource().(*modelhubv2.Media_Data); ok {
		data, _, err := inlineImageBase64(media)
		if err != nil {
			return nil, err
		}
		return map[string]string{"Base64": data}, nil
	}
	imageURL, err := provider.ResolveImageURL(media)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(imageURL, "data:") {
		// 提交前解析调用方 data URI；ParseDataURI 本身不标 Local，由此处认定。
		_, data, err := provider.ParseDataURI(imageURL)
		if err != nil {
			return nil, provider.AsNotAttempted(err)
		}
		return map[string]string{"Base64": base64.StdEncoding.EncodeToString(data)}, nil
	}
	return map[string]string{"Url": imageURL}, nil
}

// Kling 的 HTTP 200 也可能携带 Response.Error，不能把它当作受理成功。
func parseKlingSubmit(envelope map[string]any) (string, error) {
	response, _ := envelope["Response"].(map[string]any)
	taskID := firstNonEmpty(stringFieldAny(envelope, "name", "id", "task_id", "taskId"), stringField(response, "JobId"))
	if taskID == "" {
		taskID = responseTaskID(envelope)
	}
	if taskID != "" {
		return taskID, nil
	}
	if detail, ok := response["Error"].(map[string]any); ok {
		code, message := stringField(detail, "Code"), stringField(detail, "Message")
		if code != "" && message != "" {
			message = code + ": " + message
		} else {
			message = firstNonEmpty(message, code)
		}
		if message != "" {
			return "", provider.Errorf(provider.ErrorInvalidResponse, "create rejected: %s", message)
		}
	}
	return "", nil
}

func parseKlingTask(envelope map[string]any) taskPollResult {
	task := taskObject(envelope)
	response, _ := envelope["Response"].(map[string]any)
	status := taskStatus(task, envelope)
	return taskPollResult{
		state:        normalizeTaskState(status),
		videoURL:     normalizeVideoURL(firstNonEmpty(directVideoURL(task), directVideoURL(envelope), stringField(task, "ResultVideoUrl"), stringField(response, "ResultVideoUrl"), stringField(envelope, "ResultVideoUrl"), legacyResultURL(task), legacyResultURL(envelope))),
		errorCode:    taskErrorCode(task, envelope),
		errorMessage: firstNonEmpty(taskErrorMessage(task, envelope), status),
	}
}
