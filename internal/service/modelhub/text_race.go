package modelhub

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wgdl666/kangaroo/logs"
	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/infra/llmmetric"
	"github.com/wgdl666/wgModelHub/internal/provider"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

// raceLane 是同一模型的一家供应商。
type raceLane struct {
	provider string
	text     provider.TextProvider
}

type raceOutcome struct {
	final *modelhubv2.GenerateEvent
	err   error
	first time.Duration
}

// raceText 把多家文本流收成一次 GenerateStream。
// 首个有效首字决定赢家，其余 context 立即取消；调用方只看到赢家的增量。
type raceText struct {
	service  *Service
	model    string
	lanes    []raceLane
	fallback provider.TextProvider

	mu         sync.Mutex
	winnerName string
}

func (r *raceText) winner() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.winnerName
}

// claim 只允许第一家写下赢家。返回 false 表示这家已经输了。
func (r *raceText) claim(providerName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.winnerName != "" {
		return false
	}
	r.winnerName = providerName
	return true
}

// Generate 没有可比较的首字。非流式退回 default，一次成文的调用不双路付费。
func (r *raceText) Generate(ctx context.Context, model string, request *modelhubv2.GenerateRequest) (*modelhubv2.GenerateEvent, error) {
	if r.fallback == nil {
		return nil, provider.Errorf(provider.ErrorConfiguration, "model %s race has no default provider", r.model)
	}
	return r.fallback.Generate(ctx, model, request)
}

func (r *raceText) GenerateStream(ctx context.Context, model string, request *modelhubv2.GenerateRequest, emit provider.EmitEvent) (*modelhubv2.GenerateEvent, error) {
	lanes := r.prepare(request)
	if len(lanes) < 2 {
		return nil, provider.Errorf(provider.ErrorConfiguration, "model %s race requires two providers", r.model)
	}
	// 每次流式调用重新竞速。外层重试会再次进入这里，不能沿用上一轮的赢家。
	r.mu.Lock()
	r.winnerName = ""
	r.mu.Unlock()

	parent := ctx
	ctx, cancelAll := context.WithCancel(parent)
	defer cancelAll()

	outcomes := make([]raceOutcome, len(lanes))
	cancels := make([]context.CancelFunc, len(lanes))
	done := make([]chan struct{}, len(lanes))
	winnerCh := make(chan int, 1)
	started := time.Now()

	var failMu sync.Mutex
	failed := 0
	allFailed := make(chan struct{})
	var failOnce sync.Once

	for i := range lanes {
		lane := lanes[i]
		laneCtx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		done[i] = make(chan struct{})
		go func(i int, lane preparedLane) {
			defer close(done[i])
			// 首字出现前先按车道攒着。空增量不能提前放行，否则输家的 thought 或占位会混进赢家流。
			var buffered []*modelhubv2.GenerateEvent
			won := false
			laneEmit := func(event *modelhubv2.GenerateEvent) error {
				if !won {
					if winner := r.winner(); winner != "" && winner != lane.provider {
						cancel()
						return context.Canceled
					}
					if event != nil && !event.GetFinal() && llmmetric.IsFirstModelOutput(event) {
						if !r.claim(lane.provider) {
							cancel()
							return context.Canceled
						}
						outcomes[i].first = time.Since(started)
						select {
						case winnerCh <- i:
						default:
						}
						won = true
						for _, earlier := range buffered {
							if err := emit(earlier); err != nil {
								return err
							}
						}
						buffered = nil
						return emit(event)
					}
					buffered = append(buffered, event)
					return nil
				}
				if winner := r.winner(); winner != "" && winner != lane.provider {
					cancel()
					return context.Canceled
				}
				return emit(event)
			}
			final, err := lane.text.GenerateStream(laneCtx, model, lane.request, laneEmit)
			outcomes[i].final = final
			outcomes[i].err = err
			failMu.Lock()
			failed++
			if failed == len(lanes) && r.winner() == "" {
				failOnce.Do(func() { close(allFailed) })
			}
			failMu.Unlock()
		}(i, lane)
	}

	var winner int
	select {
	case winner = <-winnerCh:
	case <-allFailed:
		cancelAll()
		for i := range done {
			<-done[i]
		}
		select {
		case winner = <-winnerCh:
		default:
			r.logRace(parent, started, lanes, outcomes)
			return nil, firstRaceError(outcomes)
		}
	case <-parent.Done():
		cancelAll()
		for i := range done {
			<-done[i]
		}
		return nil, parent.Err()
	}

	for i, cancel := range cancels {
		if i != winner {
			cancel()
		}
	}
	for i := range done {
		<-done[i]
	}
	r.logRace(parent, started, lanes, outcomes)
	if err := outcomes[winner].err; err != nil {
		return outcomes[winner].final, err
	}
	return outcomes[winner].final, nil
}

func (r *raceText) logRace(ctx context.Context, started time.Time, lanes []preparedLane, outcomes []raceOutcome) {
	parts := make([]string, 0, len(lanes))
	for i, lane := range lanes {
		item := lane.provider
		if outcomes[i].first > 0 {
			item += " first_ms=" + strconv.FormatInt(outcomes[i].first.Milliseconds(), 10)
		}
		if outcomes[i].err != nil {
			item += " err=" + outcomes[i].err.Error()
		}
		parts = append(parts, item)
	}
	logs.Default().InfoContext(ctx, "modelhub_race_done",
		"model", r.model,
		"winner", r.winner(),
		"elapsed_ms", time.Since(started).Milliseconds(),
		"lanes", strings.Join(parts, "; "),
	)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(
		attribute.String("modelhub.race.winner", r.winner()),
		attribute.String("modelhub.race.lanes", strings.Join(parts, "; ")),
	)
}

func firstRaceError(outcomes []raceOutcome) error {
	for _, outcome := range outcomes {
		if outcome.err != nil && !errors.Is(outcome.err, context.Canceled) {
			return outcome.err
		}
	}
	return provider.Errorf(provider.ErrorUnavailable, "all race providers failed before the first token")
}

type preparedLane struct {
	provider string
	text     provider.TextProvider
	request  *modelhubv2.GenerateRequest
}

// prepare 为每家复制请求并套用它自己的缓存策略。两家不能改同一份 Input。
func (r *raceText) prepare(request *modelhubv2.GenerateRequest) []preparedLane {
	cfg := r.service.live.Load()
	prepared := make([]preparedLane, 0, len(r.lanes))
	for _, lane := range r.lanes {
		cloned, _ := proto.Clone(request).(*modelhubv2.GenerateRequest)
		if cloned == nil {
			cloned = request
		}
		applyTextCachingPolicy(cloned, cfg.Providers[lane.provider])
		prepared = append(prepared, preparedLane{provider: lane.provider, text: lane.text, request: cloned})
	}
	return prepared
}

// raceBinding 把配置里的 race 名单收成一个文本供应商。default 留作非流式退路。
func (s *Service) raceBinding(model string, names []string) (binding, error) {
	cfg := s.live.Load()
	lanes := make([]raceLane, 0, len(names))
	var fallback provider.TextProvider
	defaultProvider := cfg.ModelRoutes()[model]
	for _, name := range names {
		laneBinding, err := s.textBinding(model, name)
		if err != nil {
			return binding{}, err
		}
		if name == defaultProvider {
			fallback = laneBinding.set.Text
		}
		lanes = append(lanes, raceLane{provider: name, text: laneBinding.set.Text})
	}
	if fallback == nil && len(lanes) > 0 {
		fallback = lanes[0].text
		defaultProvider = lanes[0].provider
	}
	return binding{
		set:      provider.Set{Text: &raceText{service: s, model: model, lanes: lanes, fallback: fallback}},
		model:    model,
		provider: defaultProvider,
	}, nil
}

// textBinding 按实例名取已启动的文本客户端，并确认当前配置仍声明这个模型。
func (s *Service) textBinding(model, providerName string) (binding, error) {
	cfg := s.live.Load()
	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "model %s race provider %s is unknown", model, providerName)
	}
	declared := false
	for _, name := range providerCfg.Models {
		if strings.TrimSpace(name) == model {
			declared = true
			break
		}
	}
	if !declared {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "model %s is not declared by %s", model, providerName)
	}
	set, ok := s.providers[providerName]
	if !ok || set.Text == nil {
		return binding{}, provider.Errorf(provider.ErrorConfiguration, "model %s provider %s does not support text", model, providerName)
	}
	return binding{set: set, model: model, provider: providerName}, nil
}
