package arkvideo

import "strings"

// NormalizeParams 按任务形态收口方舟参数：文生视频与首帧共用同一模型 ID，
// 不能再拆第二套枚举。首帧官方强制 ratio=adaptive；文生视频 ratio 原样下发。
// 时长都是 4–30 或 -1，不能复用 Seedance 2.0 的 15 秒上限。
func NormalizeParams(resolution string, duration int, aspectRatio string, firstFrame bool) (res string, dur int, aspect string) {
	aspect = normalizeAspectRatio(aspectRatio)
	if firstFrame {
		aspect = "adaptive"
	}
	return normalizeResolution(resolution), normalizeDuration(duration), aspect
}

func normalizeAspectRatio(aspectRatio string) string {
	switch strings.TrimSpace(aspectRatio) {
	case "21:9", "16:9", "4:3", "1:1", "3:4", "9:16", "adaptive":
		return strings.TrimSpace(aspectRatio)
	default:
		// 文生视频官方示例默认 16:9；未设置或非法值不能再误写成 adaptive。
		return "16:9"
	}
}

func normalizeResolution(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "480p", "720p", "1080p":
		return strings.ToLower(strings.TrimSpace(resolution))
	case "4k":
		// 2.5 官方写到 1080p（10bit H.265），4k 不是可下发档。
		return "1080p"
	default:
		return "720p"
	}
}

func normalizeDuration(duration int) int {
	if duration == -1 {
		return -1
	}
	if duration <= 0 {
		return 5
	}
	if duration < 4 {
		return 4
	}
	if duration > 30 {
		return 30
	}
	return duration
}
