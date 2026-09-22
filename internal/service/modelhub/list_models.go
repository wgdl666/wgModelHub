package modelhub

import (
	"context"
	"sort"

	"github.com/wgdl666/kangaroo/logs"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
)

// ListModels 只返回当前进程 ModelRoutes 已选定的模型，类别用 CategoryOf 判定。
// 配置里有、代码没有类别的名字不进清单。
func (s *Service) ListModels(_ context.Context, request *modelhubv2.ListModelsRequest) (*modelhubv2.ListModelsResponse, error) {
	match, err := catalogFilter(request.GetCategory())
	if err != nil {
		return nil, provider.ToStatus(err)
	}
	routes := map[string]string{}
	if s != nil && s.live != nil {
		routes = s.live.Load().ModelRoutes()
	}
	ids := make([]string, 0, len(routes))
	for id := range routes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*modelhubv2.ModelInfo, 0, len(ids))
	for _, id := range ids {
		category, ok := models.CategoryOf(id)
		if !ok {
			logs.Default().Info("list_models_skip_unknown_category", "model", id)
			continue
		}
		if !match(category) {
			continue
		}
		out = append(out, &modelhubv2.ModelInfo{
			Model:    id,
			Category: protoCategory(category),
		})
	}
	return &modelhubv2.ListModelsResponse{Models: out}, nil
}

func catalogFilter(category modelhubv2.ModelCategory) (func(models.Category) bool, error) {
	switch category {
	case modelhubv2.ModelCategory_MODEL_CATEGORY_UNSPECIFIED:
		return func(c models.Category) bool { return c.Public() }, nil
	case modelhubv2.ModelCategory_MODEL_CATEGORY_LLM:
		return func(c models.Category) bool { return c == models.CategoryLLM }, nil
	case modelhubv2.ModelCategory_MODEL_CATEGORY_MULTIMODAL:
		return func(c models.Category) bool { return c == models.CategoryMultimodal }, nil
	case modelhubv2.ModelCategory_MODEL_CATEGORY_IMAGE_GENERATION:
		return func(c models.Category) bool { return c == models.CategoryImageGeneration }, nil
	case modelhubv2.ModelCategory_MODEL_CATEGORY_VIDEO_GENERATION:
		return func(c models.Category) bool { return c == models.CategoryVideoGeneration }, nil
	case modelhubv2.ModelCategory_MODEL_CATEGORY_SPEECH:
		return func(c models.Category) bool { return c == models.CategorySpeech }, nil
	default:
		return nil, provider.New(provider.ErrorInvalidArgument, "unsupported model category")
	}
}

func protoCategory(category models.Category) modelhubv2.ModelCategory {
	switch category {
	case models.CategoryLLM:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_LLM
	case models.CategoryMultimodal:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_MULTIMODAL
	case models.CategoryImageGeneration:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_IMAGE_GENERATION
	case models.CategoryVideoGeneration:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_VIDEO_GENERATION
	case models.CategorySpeech:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_SPEECH
	default:
		return modelhubv2.ModelCategory_MODEL_CATEGORY_UNSPECIFIED
	}
}
