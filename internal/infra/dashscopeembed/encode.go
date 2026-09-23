package dashscopeembed

import (
	"encoding/base64"
	"encoding/json"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
)

func encodeBase64(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func imageMime(image *modelhubv2.Media) string {
	if mime := image.GetMimeType(); mime != "" {
		return mime
	}
	return "image/jpeg"
}

func embeddingEvent(vector []float64) (*modelhubv2.GenerateEvent, error) {
	body, err := json.Marshal(map[string]any{"embedding": vector})
	if err != nil {
		return nil, provider.Wrap(provider.ErrorUnavailable, "encode embedding", err)
	}
	return provider.TextFinalEvent(string(body), nil, "", "", nil), nil
}

func emitText(event *modelhubv2.GenerateEvent, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	text := ""
	if len(event.GetItems()) > 0 {
		text = event.GetItems()[0].GetText()
	}
	if emit != nil && text != "" {
		if err := emit(provider.TextDeltaEvent(text)); err != nil {
			return nil, err
		}
	}
	return provider.MetadataFinalEvent("", "", nil), nil
}
