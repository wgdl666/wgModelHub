package factory

import (
	"context"
	"testing"

	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/models"
)

func TestBuildOpenAIExposesImage(t *testing.T) {
	sets, err := Build(context.Background(), config.Config{
		Providers: map[string]config.ProviderConfig{
			"ominilink_gpt_image": {
				Models: []string{models.GPTImage2},
				OpenAI: &config.OpenAIProviderConfig{
					APIKey:  "k",
					BaseURL: "https://api.ominilink.ai/v1",
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	set := sets["ominilink_gpt_image"]
	if set.Text == nil || set.Image == nil {
		t.Fatalf("openai set=%#v", set)
	}
	if set.Video != nil {
		t.Fatalf("openai should not expose video")
	}
}
