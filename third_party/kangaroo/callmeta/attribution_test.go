package callmeta

import (
	"context"
	"encoding/json"
	"go.opentelemetry.io/otel/propagation"
	"testing"
)

func TestAttributionSurvivesTaskStorageAndDoesNotLeak(t *testing.T) {
	ctx := WithAttribution(context.Background(), "mirror", "photo")
	task := map[string]any{}
	SaveTask(ctx, task)
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	restored := RestoreTask(context.Background(), stored)
	if line, scene := Attribution(restored); line != "mirror" || scene != "photo" {
		t.Fatalf("lost attribution: %s/%s", line, scene)
	}
	fitpop := WithAttribution(ctx, "fitpop", "record")
	carrier := propagation.MapCarrier{}
	Propagator{}.Inject(fitpop, carrier)
	if carrier[BusinessLine] != "fitpop" || carrier[BusinessScene] != "record" {
		t.Fatal(carrier)
	}
	if line, scene := Attribution(ctx); line != "mirror" || scene != "photo" {
		t.Fatal("parent mutated")
	}
	empty := Propagator{}.Extract(ctx, propagation.MapCarrier{})
	if line, scene := Attribution(empty); line != "" || scene != "" {
		t.Fatal("new request inherited previous attribution")
	}
	if _, exists := carrier["x-wg-caller-service"]; exists {
		t.Fatal("technical caller must be set by current client")
	}
}
