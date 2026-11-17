package modelhub

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/kangaroo/logs"
	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"google.golang.org/grpc/metadata"
)

const drillMetadataKey = "x-wg-degradation-drill-bin"

// injectDrillFault 只处理内网 Ops 演练请求，故障在访问供应商和写视频任务之前生效。
func injectDrillFault(ctx context.Context, model string) error {
	values := metadata.ValueFromIncomingContext(ctx, drillMetadataKey)
	if len(values) != 1 {
		return nil
	}
	if _, public := auth.PublicCaller(ctx); public {
		return nil
	}
	key, runID := callmeta.Business(ctx)
	if key != "load_test_run" || runID == "" {
		return nil
	}
	var drill struct {
		ModelIDs []string `json:"model_ids"`
		Mode     string   `json:"mode"`
	}
	if json.Unmarshal([]byte(values[0]), &drill) != nil || !slices.Contains(drill.ModelIDs, model) {
		return nil
	}
	switch drill.Mode {
	case "unavailable":
		logs.Default().InfoContext(ctx, "modelhub_drill_fault", "run_id", runID, "model", model, "mode", drill.Mode)
		return provider.ToStatus(provider.NotAttempted(provider.ErrorUnavailable, "degradation drill: model unavailable"))
	case "hang":
		logs.Default().InfoContext(ctx, "modelhub_drill_fault", "run_id", runID, "model", model, "mode", drill.Mode)
		// 用本次模型调用的取消信号结束挂起，保留 Wardrobe 的竞速取消行为。
		<-ctx.Done()
		return provider.ToStatus(ctx.Err())
	default:
		return nil
	}
}
