package config

import (
	"strings"
	"testing"

	"github.com/wgdl666/wgModelHub/models"
)

func validConfig() Config {
	return Config{
		Server: struct {
			ListenAddress       string
			PublicListenAddress string
			HTTPListenAddress   string
		}{ListenAddress: ":50053", HTTPListenAddress: ":51053"},
		Logfire: LogfireConfig{
			Token:   "logfire-token",
			Env:     "production",
			Service: "wg-model-hub",
		},
		Database: DatabaseConfig{DSN: "postgres://modelhub:modelhub@127.0.0.1:5432/modelhub?sslmode=disable"},
		Providers: map[string]ProviderConfig{
			"ark": {
				Models: []string{"doubao-chat"},
				Ark:    &ArkProviderConfig{APIKey: "key"},
			},
			"gemini": {
				Models: []string{models.Gemini25Flash, models.Gemini25FlashImage},
				Gemini: &GeminiProviderConfig{APIKey: "key"},
			},
			"ltx": {
				Models: []string{models.VWorldOutfitVideo10},
				LTX: &LTXProviderConfig{
					BaseURL:      "https://ltx.example",
					Token:        "token",
					Duration:     4,
					FPS:          24,
					PollInterval: 2,
				},
			},
		},
	}
}

func TestValidateAcceptsUniqueModels(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsDuplicateModelsWithoutRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["other"] = ProviderConfig{
		Models: []string{models.Gemini25Flash},
		OpenAI: &OpenAIProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "model_routes") {
		t.Fatalf("expected model_routes required error, got %v", err)
	}
}

func TestValidateAcceptsDuplicateModelsWithExplicitRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "aws_gemini",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	routes := cfg.ModelRoutes()
	if routes[models.Gemini25FlashImage] != "aws_gemini" {
		t.Fatalf("expected aws_gemini route, got %#v", routes)
	}
	if routes[models.Gemini25Flash] != "gemini" {
		t.Fatalf("single-provider model should keep implicit route, got %#v", routes)
	}
}

func TestValidateRejectsUnknownRouteProvider(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "missing",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("expected unknown provider error, got %v", err)
	}
}

func TestValidateRejectsRouteProviderMissingModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["aws_gemini"] = ProviderConfig{
		Models: []string{models.Gemini25FlashImage},
		Gemini: &GeminiProviderConfig{APIKey: "aws-key"},
	}
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini25FlashImage: "ark",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("expected undeclared model route error, got %v", err)
	}
}

func TestValidateRejectsRouteForUndeclaredModel(t *testing.T) {
	cfg := validConfig()
	cfg.ModelRouteOverrides = map[string]string{
		models.Gemini31FlashImage: "gemini",
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "not declared by any provider") {
		t.Fatalf("expected undeclared model error, got %v", err)
	}
}

func TestValidateRejectsEmptyModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark"] = ProviderConfig{
		Models: []string{"  "},
		Ark:    &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "empty model") {
		t.Fatalf("expected empty model error, got %v", err)
	}
}

func TestValidateRejectsProviderWithoutModels(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark"] = ProviderConfig{
		Ark: &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "models are required") {
		t.Fatalf("expected models required error, got %v", err)
	}
}

func TestValidateRejectsEmptyDSN(t *testing.T) {
	cfg := validConfig()
	cfg.Database.DSN = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "database.dsn") {
		t.Fatalf("expected database.dsn error, got %v", err)
	}
}

func TestValidateRejectsMixedProviderKinds(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["mixed"] = ProviderConfig{
		Models: []string{"mixed-model"},
		Gemini: &GeminiProviderConfig{APIKey: "key"},
		Ark:    &ArkProviderConfig{APIKey: "key"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed provider error, got %v", err)
	}
}

func TestModelRoutes(t *testing.T) {
	routes := validConfig().ModelRoutes()
	if routes[models.Gemini25Flash] != "gemini" || routes[models.VWorldOutfitVideo10] != "ltx" {
		t.Fatalf("unexpected routes %#v", routes)
	}
}

func TestProviderSupportsSpeech(t *testing.T) {
	tts := ProviderConfig{MinimaxTTS: &MinimaxTTSProviderConfig{APIKey: "k"}}
	if !ProviderSupports(tts, CapabilitySpeech) {
		t.Fatal("minimax_tts should support speech")
	}
	if ProviderSupports(tts, CapabilityText) || ProviderSupports(tts, CapabilityImage) || ProviderSupports(tts, CapabilityVideo) {
		t.Fatal("minimax_tts should not support text/image/video")
	}
	eleven := ProviderConfig{ElevenLabsTTS: &ElevenLabsTTSProviderConfig{APIKey: "k", VoiceID: "v"}}
	if !ProviderSupports(eleven, CapabilitySpeech) {
		t.Fatal("elevenlabs_tts should support speech")
	}
}

func TestProviderSupportsASRWithoutReusingSpeech(t *testing.T) {
	asr := ProviderConfig{SonioxASR: &SonioxASRProviderConfig{APIKey: "k"}}
	if !ProviderSupports(asr, CapabilityASR) {
		t.Fatal("soniox must support ASR")
	}
	if ProviderSupports(asr, CapabilitySpeech) {
		t.Fatal("ASR provider must not support TTS speech")
	}
}

func TestModelRoutesSkipsASRWithoutCredentials(t *testing.T) {
	cfg := Config{Providers: map[string]ProviderConfig{
		"disabled": {Models: []string{"stt-rt-v5"}, SonioxASR: &SonioxASRProviderConfig{}},
		"enabled":  {Models: []string{"nova-3"}, DeepgramASR: &DeepgramASRProviderConfig{APIKey: "k"}},
	}}
	routes := cfg.ModelRoutes()
	if _, ok := routes["stt-rt-v5"]; ok {
		t.Fatal("ASR without credentials must not bind a model")
	}
	if routes["nova-3"] != "enabled" {
		t.Fatalf("routes=%v", routes)
	}
}

func TestProviderSupportsOpenAIImage(t *testing.T) {
	openai := ProviderConfig{OpenAI: &OpenAIProviderConfig{APIKey: "k"}}
	if !ProviderSupports(openai, CapabilityText) || !ProviderSupports(openai, CapabilityImage) {
		t.Fatalf("openai should support text and image")
	}
	if ProviderSupports(openai, CapabilityVideo) {
		t.Fatalf("openai should not support video")
	}
}

func TestProviderSupportsPhotoroomImageOnly(t *testing.T) {
	photoroom := ProviderConfig{Photoroom: &PhotoroomProviderConfig{APIKey: "k"}}
	if !ProviderSupports(photoroom, CapabilityImage) {
		t.Fatal("photoroom should support image")
	}
	if ProviderSupports(photoroom, CapabilityText) || ProviderSupports(photoroom, CapabilityVideo) || ProviderSupports(photoroom, CapabilitySpeech) {
		t.Fatal("photoroom should only support image")
	}
}

func TestProviderSupportsSegmentPersonImageOnly(t *testing.T) {
	segment := ProviderConfig{SegmentPerson: &SegmentPersonProviderConfig{BaseURL: "https://segment.example", Method: "person-bria-rmbg"}}
	if !ProviderSupports(segment, CapabilityImage) {
		t.Fatal("segment_person should support image")
	}
	if ProviderSupports(segment, CapabilityText) || ProviderSupports(segment, CapabilityVideo) || ProviderSupports(segment, CapabilitySpeech) {
		t.Fatal("segment_person should only support image")
	}
}

func TestProviderSupportsVWorldImageOnly(t *testing.T) {
	vworld := ProviderConfig{VWorldImage: &VWorldImageProviderConfig{BaseURL: "https://vmind-image.model.wgdl.tech", Username: "wgdl", Password: "secret"}}
	if !ProviderSupports(vworld, CapabilityImage) {
		t.Fatal("vworld_image should support image")
	}
	if ProviderSupports(vworld, CapabilityText) || ProviderSupports(vworld, CapabilityVideo) || ProviderSupports(vworld, CapabilitySpeech) {
		t.Fatal("vworld_image should only support image")
	}
}

func TestValidateRejectsVWorldImageWithoutPassword(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["vworld_flux_image"] = ProviderConfig{
		Models:      []string{models.VWorldWardrobe10},
		VWorldImage: &VWorldImageProviderConfig{BaseURL: "https://vmind-image.model.wgdl.tech", Username: "wgdl"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("expected password error, got %v", err)
	}
}

func TestValidateRejectsSegmentPersonWithoutMethod(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["segment_person_bria"] = ProviderConfig{
		Models:        []string{models.SegmentPersonBria},
		SegmentPerson: &SegmentPersonProviderConfig{BaseURL: "https://segment.example"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "method") {
		t.Fatalf("expected method error, got %v", err)
	}
}

func TestValidateRejectsPhotoroomWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["photoroom_bg"] = ProviderConfig{
		Models:    []string{models.PhotoroomSegment},
		Photoroom: &PhotoroomProviderConfig{},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestValidateRejectsVertexAIWithoutAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["vertex_chat"] = ProviderConfig{
		Models:   []string{models.Gemini20Flash001},
		VertexAI: &VertexAIProviderConfig{},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestValidateRejectsVertexAIBlankAPIKey(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["vertex_chat"] = ProviderConfig{
		Models:   []string{models.Gemini20Flash001},
		VertexAI: &VertexAIProviderConfig{APIKey: "   "},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected api_key error, got %v", err)
	}
}

func TestVertexImageCapabilityFollowsRequestedModel(t *testing.T) {
	chat := ProviderConfig{
		Models:   []string{models.Gemini25Flash, models.Gemini20Flash001},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key"},
	}
	if !ProviderSupports(chat, CapabilityText) || !ProviderSupports(chat, CapabilityImage) {
		t.Fatal("vertex client image capability must not depend on the models list")
	}
	mixed := ProviderConfig{
		Models:   []string{models.Gemini20Flash001, models.GeminiNanoBanana21},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key"},
	}
	if !VertexImageRequestAllowed(mixed, models.GeminiNanoBanana21, CapabilityImage) {
		t.Fatal("image model request must be allowed")
	}
	if VertexImageRequestAllowed(mixed, models.Gemini20Flash001, CapabilityImage) {
		t.Fatal("text model must not gain image because a sibling image model is declared")
	}
	if !VertexImageRequestAllowed(chat, models.GeminiNanoBanana21, CapabilityImage) {
		t.Fatal("allowance follows the requested model, not the instance models list")
	}
	if ProviderSupports(mixed, CapabilityVideo) {
		t.Fatal("vertex must not support video")
	}
	image := ProviderConfig{
		Models:   []string{models.GeminiNanoBanana21},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key"},
	}
	routes := Config{Providers: map[string]ProviderConfig{
		"vertex_chat":        chat,
		"vertex_nano_banana": image,
		"gemini": {
			Models: []string{models.Gemini25Flash},
			Gemini: &GeminiProviderConfig{APIKey: "key"},
		},
	}, ModelRouteOverrides: map[string]string{
		models.Gemini25Flash: "gemini",
	}}.ModelRoutes()
	if routes[models.Gemini25Flash] != "gemini" {
		t.Fatalf("gemini-2.5-flash route=%q", routes[models.Gemini25Flash])
	}
	if routes[models.GeminiNanoBanana21] != "vertex_nano_banana" {
		t.Fatalf("nano banana route=%q", routes[models.GeminiNanoBanana21])
	}
	if routes[models.Gemini20Flash001] != "vertex_chat" {
		t.Fatalf("gemini-2.0 route=%q", routes[models.Gemini20Flash001])
	}
}

func TestValidateAcceptsVertexAIAPIKeyOnly(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["vertex_chat"] = ProviderConfig{
		Models:   []string{models.Gemini20Flash001},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAcceptsVertexProjectAndRejectsBlank(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["vertex_nano_banana"] = ProviderConfig{
		Models:   []string{models.GeminiNanoBanana21},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key", Project: "123"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{" ", "123/global", "123 456"} {
		cfg.Providers["vertex_nano_banana"] = ProviderConfig{
			Models:   []string{models.GeminiNanoBanana21},
			VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key", Project: project},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("project %q must be rejected", project)
		}
	}
	cfg.Providers["vertex_nano_banana"] = ProviderConfig{
		Models:   []string{models.GeminiNanoBanana21},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key", Project: "123", ProxyURL: "http://127.0.0.1:1081"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["vertex_nano_banana"] = ProviderConfig{
		Models:   []string{models.GeminiNanoBanana21},
		VertexAI: &VertexAIProviderConfig{APIKey: "vertex-express-key", Project: "123", ProxyURL: "not a url"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid proxy")
	}
}

func TestValidateAcceptsElevenLabsVoiceSettings(t *testing.T) {
	stability, similarity, style, speed := 0.30, 0.30, 0.50, 0.90
	cfg := validConfig()
	cfg.Providers["elevenlabs_tts"] = ProviderConfig{
		Models: []string{models.ElevenFlashV25},
		ElevenLabsTTS: &ElevenLabsTTSProviderConfig{
			APIKey: "k", VoiceID: "v",
			Stability: &stability, SimilarityBoost: &similarity, Style: &style, Speed: &speed,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsElevenLabsVoiceSettingsOutOfRange(t *testing.T) {
	bad := 1.5
	cfg := validConfig()
	cfg.Providers["elevenlabs_tts"] = ProviderConfig{
		Models: []string{models.ElevenFlashV25},
		ElevenLabsTTS: &ElevenLabsTTSProviderConfig{
			APIKey: "k", VoiceID: "v", Stability: &bad,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "stability") {
		t.Fatalf("expected stability error, got %v", err)
	}
}

func TestValidateRejectsMixedPhotoroomAndOpenAI(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["mixed"] = ProviderConfig{
		Models:    []string{models.PhotoroomSegment},
		Photoroom: &PhotoroomProviderConfig{APIKey: "k"},
		OpenAI:    &OpenAIProviderConfig{APIKey: "k"},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed provider error, got %v", err)
	}
}

func TestProviderSupportsVideoProviders(t *testing.T) {
	cases := []ProviderConfig{
		{DashScopeVideo: &DashScopeVideoProviderConfig{APIKey: "k"}},
		{OminilinkVideo: &OminilinkVideoProviderConfig{APIKey: "k"}},
		{GeminiVideo: &GeminiVideoProviderConfig{APIKey: "k"}},
		{ArkVideo: &ArkVideoProviderConfig{APIKey: "k"}},
		{LTX: &LTXProviderConfig{BaseURL: "https://ltx", Duration: 4, FPS: 24, PollInterval: 1}},
	}
	for i, provider := range cases {
		if !ProviderSupports(provider, CapabilityVideo) {
			t.Fatalf("provider %d should support video", i)
		}
		if ProviderSupports(provider, CapabilityText) || ProviderSupports(provider, CapabilityImage) {
			t.Fatalf("provider %d should only support video", i)
		}
	}
}

func TestValidateRejectsArkEndpointIDWithMultipleModels(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark_doubao_mini"] = ProviderConfig{
		Models: []string{models.DoubaoSeed20Mini, models.DoubaoSeed16},
		Ark: &ArkProviderConfig{
			APIKey:     "key",
			EndpointID: "ep-20260901122606-bcxpg",
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "endpoint_id requires exactly one model") {
		t.Fatalf("expected endpoint_id single-model error, got %v", err)
	}
}

func TestValidateAcceptsArkEndpointIDWithSingleModel(t *testing.T) {
	cfg := validConfig()
	cfg.Providers["ark_doubao_mini"] = ProviderConfig{
		Models: []string{models.DoubaoSeed20Mini},
		Ark: &ArkProviderConfig{
			APIKey:     "key",
			EndpointID: "ep-20260901122606-bcxpg",
		},
	}
	cfg.Providers["ark_doubao_lite"] = ProviderConfig{
		Models: []string{models.DoubaoSeed20Lite},
		Ark: &ArkProviderConfig{
			APIKey:     "key",
			EndpointID: "ep-20260901133933-xqknf",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
