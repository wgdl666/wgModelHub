package openai

import (
	"context"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"os"
	"strings"
	"testing"
	"time"
)

// 显式注入凭据才发真实请求；默认测试不计费，不从开发机自动寻找密钥。
func TestGPTLive(t *testing.T) {
	key := os.Getenv("MODELHUB_GPT_LIVE_KEY")
	if key == "" {
		t.Skip("MODELHUB_GPT_LIVE_KEY is unset")
	}
	base := os.Getenv("MODELHUB_GPT_LIVE_BASE_URL")
	if base == "" {
		t.Fatal("MODELHUB_GPT_LIVE_BASE_URL is required")
	}
	p, err := New("gpt_live", key, base, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{models.GPT56Sol, models.GPT56Luna, models.GPT56Terra, models.GPT6Astra, models.GPT6Sol, models.GPT6Luna, models.GPT61Sol} {
		t.Run(id, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			req := systemUserRequest("Be concise.", "Reply with exactly OK.", nil)
			limit := int32(256)
			req.GetOutput().GetText().MaxOutputTokens = &limit
			event, err := p.Generate(ctx, id, req)
			if err != nil {
				t.Fatal(err)
			}
			if event == nil || !event.Final || len(event.Items) == 0 || strings.TrimSpace(event.Items[0].GetText()) != "OK" {
				t.Fatal("missing final OK text")
			}
		})
	}
	t.Run("stream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var content strings.Builder
		_, err := p.GenerateStream(ctx, models.GPT61Sol, systemUserRequest("Be concise.", "Reply with exactly OK.", nil), func(e *modelhubv2.GenerateEvent) error {
			for _, item := range e.Items {
				content.WriteString(item.GetText())
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(content.String()) != "OK" {
			t.Fatal("missing stream delta")
		}
	})
}
