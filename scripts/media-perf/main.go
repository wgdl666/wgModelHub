// media-perf compares real adapter/ledger code against a local HTTP upstream.
// Run one scenario per fresh container; see docs/performance/media-memory.md.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
	"github.com/wgdl666/wgModelHub/internal/infra/humanparser"
	"github.com/wgdl666/wgModelHub/internal/infra/humanyolo"
	"github.com/wgdl666/wgModelHub/internal/infra/segmentperson"
	"github.com/wgdl666/wgModelHub/models"
)

type barrier struct{ started, recorded, release chan struct{} }

type store struct {
	hold             atomic.Pointer[barrier]
	wg               sync.WaitGroup
	records, updates atomic.Int64
}

func (s *store) Record(context.Context, callledger.Record) error {
	s.records.Add(1)
	if b := s.hold.Load(); b != nil {
		b.recorded <- struct{}{}
	}
	return nil
}
func (s *store) UpdateResult(context.Context, callledger.Record) error { s.updates.Add(1); return nil }
func (s *store) UpsertByGenerationTask(context.Context, callledger.Record) error {
	panic("unexpected video task")
}
func (s *store) Delete(context.Context, string) error { panic("unexpected abort") }
func (s *store) Put(_ context.Context, key, _ string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("empty media upload")
	}
	return "s3://local/" + key, nil // Consume without retaining the upload buffer.
}
func (s *store) launch(fn func()) { s.wg.Add(1); go func() { defer s.wg.Done(); fn() }() }
func (s *store) waitUpstream() {
	if b := s.hold.Load(); b != nil {
		b.started <- struct{}{}
		<-b.release
		return
	}
	time.Sleep(50 * time.Millisecond)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func cpuSeconds() float64 {
	var usage syscall.Rusage
	check(syscall.Getrusage(syscall.RUSAGE_SELF, &usage))
	return float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
}
func request(data []byte) *modelhubv2.GenerateRequest {
	return &modelhubv2.GenerateRequest{Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Parts: []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: "image/png", Source: &modelhubv2.Media_Data{Data: data}}}}}}}}}}}
}

func main() {
	scenario := flag.String("scenario", "multipart", "multipart, base64-form, base64-json, ledger")
	concurrency := flag.Int("concurrency", 1, "concurrent requests")
	count := flag.Int("requests", 32, "measured completed requests")
	fixture := flag.String("image", "/fixture.png", "local image")
	flag.Parse()
	if *concurrency < 1 || *count < *concurrency || *count%*concurrency != 0 {
		panic("requests must be a positive multiple of concurrency")
	}
	image, err := os.ReadFile(*fixture)
	check(err)
	s := &store{}
	var requests, requestBytes atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if n != r.ContentLength {
			http.Error(w, "incomplete body", 400)
			return
		}
		requests.Add(1)
		requestBytes.Add(n)
		s.waitUpstream()
		w.Header().Set("Content-Type", "application/json")
		if *scenario == "base64-form" {
			_, err = io.WriteString(w, `{"refined_png_b64":"cG5n"}`)
		} else {
			_, err = io.WriteString(w, `{"success":true,"persons":[]}`)
		}
		check(err)
	}))
	defer upstream.Close()
	yolo, err := humanyolo.New("local", upstream.URL, "", "")
	check(err)
	segment, err := segmentperson.New("local", upstream.URL, "", "", "birefnet")
	check(err)
	parser, err := humanparser.New("local", upstream.URL, "", "")
	check(err)
	invoke := func() {
		// Each call owns an input buffer, as separately decoded RPC requests do.
		req := request(bytes.Clone(image))
		payload, blobs := callledger.BuildGenerateInput(req)
		rec := &callledger.Record{CallID: "local", InputPayload: payload}
		call := callledger.Open(context.Background(), s, s, rec, blobs, s.launch)
		switch *scenario {
		case "multipart":
			_, err := yolo.Generate(context.Background(), models.HumanYOLO, req)
			check(err)
		case "base64-form":
			_, err := segment.GenerateImage(context.Background(), models.SegmentPersonBria, req)
			check(err)
		case "base64-json":
			_, err := parser.Generate(context.Background(), models.HumanParse, req)
			check(err)
		case "ledger":
			s.waitUpstream()
		default:
			panic("unknown scenario")
		}
		call.Finish(&callledger.Record{CallID: "local", Status: callledger.StatusSucceeded}, nil)
		runtime.KeepAlive(req)
		runtime.KeepAlive(call)
	}
	// Warm caches and connections before collecting allocation and CPU deltas.
	for range 2 {
		invoke()
	}
	s.wg.Wait()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpuStart, start := cpuSeconds(), time.Now()
	latencies := make([]float64, *count)
	var workers sync.WaitGroup
	for worker := 0; worker < *concurrency; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for i := worker; i < *count; i += *concurrency {
				start := time.Now()
				invoke()
				latencies[i] = float64(time.Since(start)) / float64(time.Millisecond)
			}
		}(worker)
	}
	workers.Wait()
	s.wg.Wait()
	elapsed, cpu := time.Since(start), cpuSeconds()-cpuStart
	runtime.ReadMemStats(&after)
	sort.Float64s(latencies)
	// A separate forced-GC snapshot holds all calls after input upload and
	// complete upstream receipt, while waiting for the model's response.
	runtime.GC()
	var idle, held runtime.MemStats
	runtime.ReadMemStats(&idle)
	b := &barrier{make(chan struct{}, *concurrency), make(chan struct{}, *concurrency), make(chan struct{})}
	s.hold.Store(b)
	for range *concurrency {
		workers.Add(1)
		go func() { defer workers.Done(); invoke() }()
	}
	for range *concurrency {
		<-b.started
		<-b.recorded
	}
	runtime.GC()
	runtime.ReadMemStats(&held)
	close(b.release)
	workers.Wait()
	s.wg.Wait()
	s.hold.Store(nil)
	if s.records.Load() != int64(2+*count+*concurrency) || s.updates.Load() != s.records.Load() {
		panic("ledger writes incomplete")
	}
	peak, err := os.ReadFile("/sys/fs/cgroup/memory.peak")
	check(err)
	expectedHTTP := int64(2 + *count + *concurrency)
	if *scenario == "ledger" {
		expectedHTTP = 0
	}
	if requests.Load() != expectedHTTP {
		panic("unexpected upstream retry or missing request")
	}
	check(json.NewEncoder(os.Stdout).Encode(map[string]any{
		"scenario": *scenario, "concurrency": *concurrency, "completed": *count,
		"go_version": runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "image_bytes": len(image), "image_sha256": fmt.Sprintf("%x", sha256.Sum256(image)),
		"elapsed_ms": float64(elapsed) / float64(time.Millisecond), "cpu_ms_per_request": cpu * 1000 / float64(*count),
		"allocated_bytes_per_request": float64(after.TotalAlloc-before.TotalAlloc) / float64(*count), "allocs_per_request": float64(after.Mallocs-before.Mallocs) / float64(*count),
		"gc_cycles": after.NumGC - before.NumGC, "gc_pause_ms": float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6,
		"p50_ms": latencies[len(latencies)/2], "p95_ms": latencies[(len(latencies)-1)*95/100],
		"idle_heap_bytes": idle.HeapAlloc, "waiting_heap_bytes": held.HeapAlloc, "waiting_heap_delta_bytes": int64(held.HeapAlloc) - int64(idle.HeapAlloc),
		"container_peak_bytes": string(bytes.TrimSpace(peak)), "upstream_requests_including_setup": requests.Load(), "upstream_bytes_including_setup": requestBytes.Load(),
	}))
}
