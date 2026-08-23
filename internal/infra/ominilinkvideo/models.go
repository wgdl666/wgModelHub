package ominilinkvideo

import "strings"

type ModelFamily string

const (
	FamilySeedance ModelFamily = "seedance"
	FamilyVeo      ModelFamily = "veo"
	FamilyKling    ModelFamily = "kling"
	FamilyVidu     ModelFamily = "vidu"
)

func normalizeModelID(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func ModelFamilyOf(model string) ModelFamily {
	model = normalizeModelID(model)
	switch {
	case strings.HasPrefix(model, "dreamina-seedance"):
		return FamilySeedance
	case strings.HasPrefix(model, "veo-3.1"):
		return FamilyVeo
	case strings.HasPrefix(model, "kling-"):
		return FamilyKling
	case strings.HasPrefix(model, "viduq3"):
		return FamilyVidu
	default:
		return ""
	}
}

func klingAPIVersion(model string) string {
	switch normalizeModelID(model) {
	case "kling-v2-master":
		return "v2.0"
	case "kling-v2-1-master":
		return "v2.1m"
	case "kling-v2-5-turbo":
		return "v2.5"
	case "kling-v2-6":
		return "v2.6"
	case "kling-v3", "kling-v3-omni", "kling-video-o1":
		return "v3.0"
	default:
		return ""
	}
}
