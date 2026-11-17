package modelhub

import (
	"context"
	"testing"
	"time"

	"github.com/wgdl666/kangaroo/callmeta"
	"github.com/wgdl666/wgModelHub/config"
	"github.com/wgdl666/wgModelHub/internal/auth"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGenerateDrillOnlyCutsSelectedInternalCall(t *testing.T) {
	text := &recordingText{}
	service := newTestService(config.Config{
		Providers: map[string]config.ProviderConfig{
			"gemini": {Models: []string{models.Gemini37Flash, models.Gemini38Flash}, Gemini: &config.GeminiProviderConfig{APIKey: "test"}},
		},
	}, map[string]provider.Set{"gemini": {Text: text}}, nil)
	base := callmeta.WithBusiness(context.Background(), "load_test_run", "run-1")
	ctx := metadata.NewIncomingContext(base, metadata.Pairs(drillMetadataKey, `{"model_ids":["gemini-3.7-flash"],"mode":"unavailable"}`))
	request := textRequest(models.Gemini37Flash, "hi")
	if err := service.Generate(request, &generateRecorder{ctx: ctx}); status.Code(err) != codes.Unavailable || text.request != nil {
		t.Fatalf("选中模型应在发给供应商前返回 Unavailable，err=%v", err)
	}
	if err := service.Generate(textRequest(models.Gemini38Flash, "hi"), &generateRecorder{ctx: ctx}); err != nil {
		t.Fatalf("同一请求的其他模型仍可调用: %v", err)
	}
	if err := service.Generate(request, &generateRecorder{ctx: base}); err != nil {
		t.Fatalf("平行请求没有演练标记: %v", err)
	}
	publicCtx := auth.ContextWithPublicPrincipal(ctx, "principal", "key")
	if err := service.Generate(request, &generateRecorder{ctx: publicCtx}); err != nil {
		t.Fatalf("公网请求的演练标记不得生效: %v", err)
	}
}

func TestVideoDrillCancelsWithoutSubmitting(t *testing.T) {
	video, store := &fakeVideo{}, newMemoryStore()
	service := videoService(video, store)
	base := callmeta.WithBusiness(context.Background(), "load_test_run", "run-1")
	ctx := metadata.NewIncomingContext(base, metadata.Pairs(drillMetadataKey, `{"model_ids":["Vmind_outfit_video_v1.0"],"mode":"hang"}`))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitGeneration(ctx, videoSubmitRequest(models.VWorldOutfitVideo10, "drill"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("未取消的调用应保持挂起，err=%v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if status.Code(err) != codes.Canceled || video.submitCount != 0 || len(store.byID) != 0 {
			t.Fatalf("取消挂起后不应创建视频任务，err=%v submissions=%d rows=%d", err, video.submitCount, len(store.byID))
		}
	case <-time.After(time.Second):
		t.Fatal("模型调用取消后仍未退出")
	}
}
