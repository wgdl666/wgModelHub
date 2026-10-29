package modelhub

import (
	"context"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc/metadata"
)

// headerRecorder 记下服务端发出的响应头，用来核对 call_id 与账本行是同一个。
type headerRecorder struct {
	generateRecorder
	headers []metadata.MD
}

func (r *headerRecorder) SendHeader(md metadata.MD) error {
	r.headers = append(r.headers, md)
	return nil
}

func (r *headerRecorder) callID(t *testing.T) string {
	t.Helper()
	if len(r.headers) != 1 {
		t.Fatalf("headers=%v, want exactly one", r.headers)
	}
	values := r.headers[0].Get(protocol.CallIDMetadataKey)
	if len(values) != 1 || values[0] == "" {
		t.Fatalf("call id header=%v", r.headers[0])
	}
	return values[0]
}

func textLedgerEvent() *modelhubv2.GenerateEvent {
	return &modelhubv2.GenerateEvent{Items: []*modelhubv2.OutputItem{{Item: &modelhubv2.OutputItem_Text{Text: "ok"}}}}
}

func imageRequest(model string) *modelhubv2.GenerateRequest {
	request := textRequest(model, "白衬衫")
	request.Output = &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{Image: &modelhubv2.ImageOutput{}}}
	return request
}

// 文本成功、供应商报错都在第一时间发出 call_id，并且和账本行一致。
func TestGenerateTextAnnouncesLedgerCallID(t *testing.T) {
	for name, text := range map[string]*ledgerTextProvider{
		"success":        {event: textLedgerEvent()},
		"provider error": {err: provider.New(provider.ErrorInvalidResponse, "upstream broke")},
	} {
		t.Run(name, func(t *testing.T) {
			mem := &callledger.Memory{}
			svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: text}}, nil, mem)
			stream := &headerRecorder{generateRecorder: generateRecorder{ctx: context.Background()}}
			_ = svc.Generate(textRequest("m", "hi"), stream)
			callID := stream.callID(t)
			if len(mem.Records) != 1 || mem.Records[0].CallID != callID {
				t.Fatalf("records=%#v header=%s", mem.Records, callID)
			}
		})
	}
}

func TestGenerateImageAnnouncesLedgerCallID(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{err: provider.New(provider.ErrorUnavailable, "queue is full")}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Image: img}}, nil, mem)
	stream := &headerRecorder{generateRecorder: generateRecorder{ctx: context.Background()}}
	if err := svc.Generate(imageRequest("m"), stream); err == nil {
		t.Fatal("expected provider error")
	}
	callID := stream.callID(t)
	if len(mem.Records) != 1 || mem.Records[0].CallID != callID {
		t.Fatalf("records=%#v header=%s", mem.Records, callID)
	}
}

// 本地拒识时头已经发出，但账本行被撤销：调用方拿到的 call_id 查不到，这是 Ops 显示"未触达供应商"的依据。
func TestGenerateAnnouncesCallIDEvenWhenRecordIsAborted(t *testing.T) {
	mem := &callledger.Memory{}
	img := &ledgerImageProvider{err: provider.NotAttempted(provider.ErrorInvalidArgument, "image prompt is required")}
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Image: img}}, nil, mem)
	stream := &headerRecorder{generateRecorder: generateRecorder{ctx: context.Background()}}
	if err := svc.Generate(imageRequest("m"), stream); err == nil {
		t.Fatal("expected local rejection")
	}
	if stream.callID(t) == "" || len(mem.Records) != 0 {
		t.Fatalf("records=%#v", mem.Records)
	}
}

// 没开账本时不发 call_id，调用方不会拿到一个永远查不到的编号。
func TestGenerateWithoutLedgerSendsNoCallID(t *testing.T) {
	svc := newLedgerService(textLedgerCFG("m"), map[string]provider.Set{"p": {Text: &ledgerTextProvider{event: textLedgerEvent()}}}, nil, nil)
	stream := &headerRecorder{generateRecorder: generateRecorder{ctx: context.Background()}}
	if err := svc.Generate(textRequest("m", "hi"), stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.headers) != 0 {
		t.Fatalf("headers=%v", stream.headers)
	}
}
