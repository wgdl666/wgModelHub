package ominilinkvideo

import (
	"math"
	"strings"
)

func NormalizeParams(model, resolution string, duration int, aspectRatio string) (res string, dur int, aspect string) {
	model = normalizeModelID(model)
	if duration <= 0 {
		duration = 5
	}
	aspectRatio = strings.TrimSpace(aspectRatio)
	if aspectRatio == "" {
		aspectRatio = "3:4"
	}
	switch ModelFamilyOf(model) {
	case FamilyVeo:
		return normalizeVeoParams(model, resolution, duration, aspectRatio)
	case FamilySeedance:
		return normalizeSeedanceParams(model, resolution, duration, aspectRatio)
	case FamilyKling:
		return normalizeKlingParams(model, resolution, duration, aspectRatio)
	case FamilyVidu:
		return normalizeViduParams(model, resolution, duration, aspectRatio)
	default:
		return "720p", duration, aspectRatio
	}
}

func normalizeVeoParams(model, resolution string, duration int, aspectRatio string) (string, int, string) {
	res := strings.ToLower(strings.TrimSpace(resolution))
	switch res {
	case "1080p":
		res = "1080p"
	case "4k":
		if strings.Contains(model, "lite") {
			res = "1080p"
		} else {
			res = "4k"
		}
	default:
		res = "720p"
	}
	return res, veoDurationSeconds(duration), veoAspectRatio(aspectRatio)
}

func veoDurationSeconds(duration int) int {
	switch {
	case duration <= 5:
		return 4
	case duration <= 7:
		return 6
	default:
		return 8
	}
}

func veoAspectRatio(aspectRatio string) string {
	aspectRatio = strings.TrimSpace(aspectRatio)
	if aspectRatio == "16:9" || aspectRatio == "4:3" {
		return "16:9"
	}
	return "9:16"
}

func veoResolutionForAPI(resolution string) string {
	if strings.EqualFold(strings.TrimSpace(resolution), "4k") {
		return "4K"
	}
	return resolution
}

func normalizeSeedanceParams(model, resolution string, duration int, aspectRatio string) (string, int, string) {
	res := strings.ToLower(strings.TrimSpace(resolution))
	switch res {
	case "480p", "720p", "1080p", "4k":
	default:
		res = "720p"
	}
	if isSeedanceMiniOrFast(model) && (res == "1080p" || res == "4k") {
		res = "720p"
	}
	duration = clampInt(duration, 4, 15)
	return res, duration, seedanceRatio(aspectRatio)
}

func isSeedanceMiniOrFast(model string) bool {
	model = normalizeModelID(model)
	return strings.HasSuffix(model, "-mini") || strings.HasSuffix(model, "-fast")
}

func seedanceRatio(aspectRatio string) string {
	switch strings.TrimSpace(aspectRatio) {
	case "16:9", "4:3", "1:1", "3:4", "9:16", "21:9", "adaptive":
		return aspectRatio
	default:
		return "3:4"
	}
}

func normalizeKlingParams(model, resolution string, duration int, aspectRatio string) (string, int, string) {
	return klingResolutionFromInput(model, resolution), klingDuration(model, duration), aspectRatio
}

func klingResolutionFromInput(model, resolution string) string {
	res := strings.ToLower(strings.TrimSpace(resolution))
	switch res {
	case "4k":
		if klingAPIVersion(model) == "v3.0" {
			return "4k"
		}
		return "1080p"
	case "1080p":
		return "1080p"
	default:
		return "720p"
	}
}

func klingDuration(model string, duration int) int {
	if klingAPIVersion(model) == "v3.0" {
		return clampInt(duration, 3, 15)
	}
	return snapToAllowed(duration, []int{5, 10})
}

func normalizeViduParams(model, resolution string, duration int, aspectRatio string) (string, int, string) {
	res := strings.ToLower(strings.TrimSpace(resolution))
	if strings.Contains(model, "pro-fast") {
		switch res {
		case "1080p":
			res = "1080p"
		default:
			res = "720p"
		}
	} else {
		switch res {
		case "540p", "720p", "1080p":
		default:
			res = "720p"
		}
	}
	return res, clampInt(duration, 1, 16), aspectRatio
}

func klingMode(resolution string) string {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "4k":
		return "4k"
	case "1080p":
		return "pro"
	default:
		return "std"
	}
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func snapToAllowed(value int, allowed []int) int {
	if len(allowed) == 0 {
		return value
	}
	best := allowed[0]
	bestDist := math.MaxInt
	for _, candidate := range allowed {
		dist := absInt(value - candidate)
		if dist < bestDist {
			bestDist = dist
			best = candidate
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
