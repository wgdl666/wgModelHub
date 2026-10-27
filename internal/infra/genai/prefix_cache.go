package genai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/prefixcache"
	"github.com/wgdl666/wgModelHub/internal/provider"
	genaisdk "google.golang.org/genai"
)

type staticPrefix struct {
	SystemInstruction *genaisdk.Content
	Tools             []*genaisdk.Tool
	ToolConfig        *genaisdk.ToolConfig
}

func cacheIdentity(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ConfigurePrefixCache is called during server assembly, before serving requests.
// The policy callback reads the current atomic Nacos/AppConfig snapshot.
func (p *Provider) ConfigurePrefixCache(cache *prefixcache.Manager, enabled func(string) bool) {
	p.cache = cache
	p.cacheEnabled = enabled
}

func (p *Provider) prefixKey(ctx context.Context, model string, config *genaisdk.GenerateContentConfig) string {
	region := strings.ToLower(strings.TrimSpace(os.Getenv("XX_WG_REGION")))
	if region == "" {
		region = "cn"
	}
	scope := cacheIdentity("google-prefix-v1", region, p.cacheIdentity, model, callledger.CallerFromContext(ctx))
	prefix := staticPrefix{SystemInstruction: config.SystemInstruction, Tools: config.Tools, ToolConfig: config.ToolConfig}
	encoded, err := json.Marshal(prefix)
	if err != nil {
		return ""
	}
	return cacheIdentity(scope, string(encoded))
}

func (p *Provider) preparePrefixCache(ctx context.Context, model string, request *modelhubv2.GenerateRequest, original *genaisdk.GenerateContentConfig) (*genaisdk.GenerateContentConfig, *prefixcache.Entry) {
	if p.cache == nil || p.cacheEnabled == nil || !p.cacheEnabled(model) || ctx.Err() != nil {
		return original, nil
	}
	if caching := request.GetInput().GetCaching(); caching != nil && !caching.Enabled {
		return original, nil
	}
	if original.SystemInstruction == nil && len(original.Tools) == 0 {
		return original, nil
	}
	key := p.prefixKey(ctx, model, original)
	if key == "" {
		return original, nil
	}
	if entry := p.cache.Lookup(ctx, key); entry != nil {
		cached := *original
		cached.CachedContent = entry.Name
		cached.SystemInstruction = nil
		cached.Tools = nil
		cached.ToolConfig = nil
		return &cached, entry
	}
	p.schedulePrefixCache(ctx, model, original, key)
	return original, nil
}

func (p *Provider) schedulePrefixCache(ctx context.Context, model string, original *genaisdk.GenerateContentConfig, key string) {
	if ctx.Err() != nil {
		return
	}
	// Only static, already-converted fields are retained. Dynamic messages/media
	// and the caller's canceled request context are not held by the background job.
	prefix := staticPrefix{SystemInstruction: original.SystemInstruction, Tools: original.Tools, ToolConfig: original.ToolConfig}
	p.cache.ScheduleCreate(key, func(createCtx context.Context) (*prefixcache.Entry, error) {
		if p.cacheEnabled == nil || !p.cacheEnabled(model) {
			return nil, nil
		}
		cached, err := p.client.Caches.Create(createCtx, model, &genaisdk.CreateCachedContentConfig{
			TTL:               prefixcache.TTL,
			SystemInstruction: prefix.SystemInstruction,
			Tools:             prefix.Tools,
			ToolConfig:        prefix.ToolConfig,
		})
		if err != nil {
			return nil, err
		}
		if cached == nil || cached.Name == "" || cached.ExpireTime.IsZero() {
			return nil, provider.New(provider.ErrorInvalidResponse, "cache creation returned no resource name or expiry")
		}
		return &prefixcache.Entry{Name: cached.Name, ExpiresAt: cached.ExpireTime}, nil
	})
}

func missingCachedContent(err error) bool {
	var apiError genaisdk.APIError
	if !errors.As(err, &apiError) || (apiError.Code != 400 && apiError.Code != 404) {
		return false
	}
	message := strings.ToLower(apiError.Message)
	mentionsCache := strings.Contains(message, "cached content") || strings.Contains(message, "cachedcontent") || strings.Contains(message, "cached_content")
	return mentionsCache && (strings.Contains(message, "not found") || strings.Contains(message, "not_found") || strings.Contains(message, "expired") || strings.Contains(message, "does not exist"))
}
