package ominilinkvideo

import (
	"encoding/json"
	"strings"

	"github.com/wgdl666/wgModelHub/internal/provider"
)

type taskPollResult struct {
	state        provider.VideoJobState
	videoURL     string
	errorCode    string
	errorMessage string
}

func decodeTaskResponse(raw []byte) (map[string]any, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, provider.Wrap(provider.ErrorInvalidResponse, "parse task response", err)
	}
	if code := strings.ToLower(stringField(envelope, "code")); code != "" && code != "success" && code != "0" {
		return nil, provider.Errorf(provider.ErrorUnavailable, "ominilink task failed: %s", firstNonEmpty(stringField(envelope, "message"), code))
	}
	// A failed Seedance task is a terminal state, not a retryable polling error.
	if message := apiFailureMessage(envelope); message != "" && stringField(envelope, "status") != "failed" {
		return nil, provider.Errorf(provider.ErrorUnavailable, "ominilink task failed: %s", message)
	}
	return envelope, nil
}

func taskObject(envelope map[string]any) map[string]any {
	if data, ok := envelope["data"].(map[string]any); ok && len(data) > 0 {
		return data
	}
	if response, ok := envelope["Response"].(map[string]any); ok && len(response) > 0 {
		return response
	}
	return envelope
}

func responseTaskID(envelope map[string]any) string {
	if id := stringFieldAny(envelope, "name", "id", "task_id", "taskId"); id != "" {
		return id
	}
	data, _ := envelope["data"].(map[string]any)
	return stringFieldAny(data, "id", "task_id", "taskId", "name")
}

func taskStatus(task, envelope map[string]any) string {
	return firstNonEmpty(stringFieldAny(task, "status", "state", "task_status", "taskStatus", "Status", "State", "TaskStatus"), stringFieldAny(envelope, "status", "state", "task_status", "taskStatus"))
}

func normalizeTaskState(status string) provider.VideoJobState {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "done":
		return provider.VideoJobSucceeded
	case "failed", "failure", "fail", "error", "cancelled", "canceled", "expired":
		return provider.VideoJobFailed
	default:
		return provider.VideoJobRunning
	}
}

// These flat URL aliases are used by the legacy AIG task wrapper. Protocol-specific
// containers (content, videos, creations, Response) are read in their own files.
func directVideoURL(object map[string]any) string {
	return stringFieldAny(object, "result_url", "resultUrl", "video_url", "videoUrl", "uri", "url", "URL", "path", "Path")
}

func legacyResultURL(object map[string]any) string {
	for _, key := range []string{"result", "output", "Output"} {
		result, _ := object[key].(map[string]any)
		if url := directVideoURL(result); url != "" {
			return url
		}
	}
	return ""
}

func normalizeVideoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	base := strings.TrimRight(defaultDownloadBaseURL, "/")
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	return base + "/" + raw
}

func apiFailureMessage(envelope map[string]any) string {
	if errObj, ok := envelope["error"].(map[string]any); ok {
		if msg := stringField(errObj, "message"); msg != "" {
			return msg
		}
	}
	if reason := stringField(envelope, "fail_reason"); reason != "" {
		return reason
	}
	if msg := stringField(envelope, "message"); msg != "" {
		lowerStatus := strings.ToLower(firstNonEmpty(stringFieldAny(envelope, "status", "state")))
		if lowerStatus == "failed" || lowerStatus == "failure" || lowerStatus == "error" {
			return msg
		}
	}
	return ""
}

func taskErrorCode(objs ...map[string]any) string {
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		// Seedance v3 将失败原因放在 error 对象中，保留 code 便于调用方区分拒绝与生成失败。
		if detail, ok := obj["error"].(map[string]any); ok {
			if code := stringField(detail, "code"); code != "" {
				return code
			}
		}
		if code := stringFieldAny(obj, "error_code", "errorCode", "code", "Code"); code != "" {
			return code
		}
	}
	return ""
}

func taskErrorMessage(objs ...map[string]any) string {
	for _, obj := range objs {
		if obj == nil {
			continue
		}
		if msg := apiFailureMessage(obj); msg != "" {
			return msg
		}
		if msg := stringFieldAny(obj, "fail_reason", "error_message", "errorMessage"); msg != "" {
			return msg
		}
	}
	return ""
}

func stringField(obj map[string]any, key string) string {
	v, ok := obj[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func stringFieldAny(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringField(obj, key); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
