package modelhub

import (
	"context"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
)

func TestGetModelCallMediaRejectsForeignKey(t *testing.T) {
	svc := NewWithLedger(nil, nil, nil, &callledger.Memory{})
	_, err := svc.GetModelCallMedia(context.Background(), &modelhubv2.GetModelCallMediaRequest{
		CallId:    "call-1",
		ObjectKey: "model-calls/other/in/0",
	})
	if err == nil {
		t.Fatal("foreign object key must be rejected")
	}
}

func TestListModelCallsOrdersAndHidesEval(t *testing.T) {
	mem := &callledger.Memory{}
	svc := NewWithLedger(nil, nil, nil, mem)
	ctx := context.Background()
	_ = mem.Record(ctx, callledger.Record{
		CallID: "a", Operation: callledger.OperationGenerateText, Capability: callledger.CapabilityText,
		Status: callledger.StatusSucceeded, Model: "gemini-2.5-flash", BusinessScene: "chat",
		TraceID:      "trace-a",
		InputPayload: map[string]any{"input": map[string]any{"text": "hello"}},
	})
	_ = mem.Record(ctx, callledger.Record{
		CallID: "b", BusinessScene: callledger.EvalBusinessScene, Status: callledger.StatusSucceeded,
	})
	resp, err := svc.ListModelCalls(ctx, &modelhubv2.ListModelCallsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetTotal() != 1 || len(resp.GetCalls()) != 1 || resp.GetCalls()[0].GetCallId() != "a" {
		t.Fatalf("unexpected list %+v", resp)
	}
	if !resp.GetCalls()[0].GetReplayable() {
		t.Fatal("text success should be replayable")
	}
	if resp.GetCalls()[0].GetTraceId() != "trace-a" {
		t.Fatalf("trace_id=%q", resp.GetCalls()[0].GetTraceId())
	}
}
