package arkvideo

import "strings"

// NormalizeParams 只服务 Seedance 2.5 第一刀首帧图生视频。
// 官方首帧任务强制 ratio=adaptive；时长是 4–30 或 -1，不能复用 2.0 的 15 秒上限。
func NormalizeParams(resolution string, duration int, _ string) (res string, dur int, aspect string) {
	return normalizeResolution(resolution), normalizeDuration(duration), "adaptive"
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
