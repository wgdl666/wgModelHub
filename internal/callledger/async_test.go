package callledger

import (
	"context"
	"testing"
	"time"
)

type putOnce struct {
	key  string
	body []byte
}

func (p *putOnce) Put(_ context.Context, key, _ string, body []byte) (string, error) {
	p.key = key
	p.body = append([]byte(nil), body...)
	return "s3://bucket/" + key, nil
}

func TestUpdateResultKeepsTraceAndInput(t *testing.T) {
	mem := &Memory{}
	if err := mem.Record(context.Background(), Record{
		CallID: "c", TraceID: "trace-1", Status: StatusPending,
		InputPayload: map[string]any{"prompt": "hi"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.UpdateResult(context.Background(), Record{
		CallID: "c", TraceID: "trace-2", Status: StatusSucceeded,
		OutputPayload: map[string]any{"text": "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	got := mem.Records[0]
	if got.TraceID != "trace-1" || got.Status != StatusSucceeded {
		t.Fatalf("trace/status=%q/%q", got.TraceID, got.Status)
	}
	if got.InputPayload["prompt"] != "hi" || got.OutputPayload["text"] != "ok" {
		t.Fatalf("payload=%v %v", got.InputPayload, got.OutputPayload)
	}
	if err := mem.UpdateResult(context.Background(), Record{CallID: "c", Status: StatusPending}); err != nil {
		t.Fatal(err)
	}
	if mem.Records[0].Status != StatusSucceeded {
		t.Fatalf("pending overwrote terminal status: %s", mem.Records[0].Status)
	}
}

func TestAttachBlobsStoresURI(t *testing.T) {
	store := &putOnce{}
	payload := map[string]any{"uri": "ledger://pending/in/0"}
	got := AttachBlobs(context.Background(), store, "call-1", payload, []Blob{{
		Placeholder: "ledger://pending/in/0",
		MIME:        "image/png",
		Data:        []byte("png"),
	}})
	if got["uri"] != "s3://bucket/model-calls/call-1/in/0" {
		t.Fatalf("uri=%v", got["uri"])
	}
	if store.key != "model-calls/call-1/in/0" || string(store.body) != "png" {
		t.Fatalf("uploaded key=%s body=%q", store.key, store.body)
	}
}

func TestOpenReturnsBeforeInsertFinishes(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	mem := &Memory{}
	store := &gateStore{Memory: mem, started: started, release: release}
	rec := &Record{CallID: "c", TraceID: "trace-1", InputPayload: map[string]any{"q": "x"}}
	call := Open(context.Background(), store, nil, rec, nil, func(fn func()) { go fn() })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("insert did not start")
	}
	close(release)
	done := make(chan struct{})
	go func() {
		call.Finish(&Record{CallID: "c", TraceID: "other", Status: StatusSucceeded, OutputPayload: map[string]any{"a": 1}}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finish did not return")
	}
	deadline := time.Now().Add(time.Second)
	for {
		mem.mu.Lock()
		n := len(mem.Records)
		var status, trace string
		if n == 1 {
			status = mem.Records[0].Status
			trace = mem.Records[0].TraceID
		}
		mem.mu.Unlock()
		if n == 1 && status == StatusSucceeded && trace == "trace-1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("records=%d status=%s trace=%s", n, status, trace)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type gateStore struct {
	*Memory
	started chan struct{}
	release chan struct{}
}

func (g *gateStore) Record(ctx context.Context, rec Record) error {
	if g.started != nil {
		g.started <- struct{}{}
	}
	<-g.release
	return g.Memory.Record(ctx, rec)
}
