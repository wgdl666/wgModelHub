package callledger

import (
	"context"
	"testing"
	"time"
)

func TestReplayableRequiresArchivedSuccess(t *testing.T) {
	ok, _ := Replayable(Record{
		Operation:    OperationGenerateImage,
		Status:       StatusSucceeded,
		Capability:   CapabilityImage,
		InputPayload: map[string]any{"uri": "s3://bucket/model-calls/c1/in/0"},
	})
	if !ok {
		t.Fatal("archived succeeded image should replay")
	}
	ok, reason := Replayable(Record{
		Operation:    OperationGenerateImage,
		Status:       StatusSucceeded,
		Capability:   CapabilityImage,
		InputPayload: map[string]any{"uri": "ledger://omitted"},
	})
	if ok || reason == "" {
		t.Fatalf("omitted media must stay blocked, ok=%v reason=%q", ok, reason)
	}
	ok, _ = Replayable(Record{Operation: OperationTranscribeSpeechStream, Status: StatusSucceeded, Capability: CapabilityASR})
	if ok {
		t.Fatal("asr must not replay")
	}
}

func TestMemoryListSkipsEvalAndSortsNewestFirst(t *testing.T) {
	store := &Memory{}
	base := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	_ = store.Record(context.Background(), Record{CallID: "old", StartedAt: base, BusinessScene: "ootd", Status: StatusSucceeded})
	_ = store.Record(context.Background(), Record{CallID: "new", StartedAt: base.Add(time.Hour), BusinessScene: "closet", Status: StatusSucceeded})
	_ = store.Record(context.Background(), Record{CallID: "eval", StartedAt: base.Add(2 * time.Hour), BusinessScene: EvalBusinessScene, Status: StatusSucceeded})

	rows, total, err := store.List(context.Background(), Query{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 || rows[0].CallID != "new" || rows[1].CallID != "old" {
		t.Fatalf("got total=%d ids=%v", total, idsOf(rows))
	}
	rows, total, err = store.List(context.Background(), Query{Limit: 20, IncludeEval: true})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || rows[0].CallID != "eval" {
		t.Fatalf("include eval total=%d first=%s", total, rows[0].CallID)
	}
}

func TestFirstMediaKeyIgnoresUnarchived(t *testing.T) {
	key := FirstMediaKey(map[string]any{
		"parts": []any{
			map[string]any{"uri": "ledger://pending/in/0"},
			map[string]any{"uri": "s3://bucket/model-calls/c1/out/0"},
		},
	})
	if key != "model-calls/c1/out/0" {
		t.Fatalf("key=%q", key)
	}
}

func idsOf(rows []Record) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.CallID
	}
	return out
}
