package callledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect/sql"
	"github.com/wgdl666/wgModelHub/ent"
	"github.com/wgdl666/wgModelHub/ent/modelcall"
	"github.com/wgdl666/wgModelHub/ent/predicate"
)

const (
	// EvalBusinessScene 是评测重放写入的场景。业务汇总和默认列表都排除它，避免评测流量计入真实业务量。
	EvalBusinessScene = "model_eval"
	previewRunes      = 180
)

// Query 是账本只读条件。Limit 由调用方先夹到合法范围。
type Query struct {
	From          time.Time
	To            time.Time
	Model         string
	Status        string
	CallerService string
	IncludeEval   bool
	Limit         int
	Offset        int
}

// Reader 只读账本。不并进 Store，避免只负责写入的测试替身被迫实现查询。
type Reader interface {
	List(ctx context.Context, q Query) ([]Record, int, error)
	Get(ctx context.Context, callID string) (Record, error)
}

// GenerationCallFinder 用异步视频的 task_id 找回账本 call_id。
// 评测页只打开 model-calls/{call_id}/ 下的对象，视频不能写到 task_id 路径。
type GenerationCallFinder interface {
	CallIDByGenerationTask(ctx context.Context, generationTaskID string) (string, error)
}

func (m *Memory) List(_ context.Context, q Query) ([]Record, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	matched := make([]Record, 0)
	for _, rec := range m.Records {
		if !matchQuery(rec, q) {
			continue
		}
		matched = append(matched, rec)
	}
	sortByStartedDesc(matched)
	total := len(matched)
	if q.Offset >= total {
		return nil, total, nil
	}
	end := total
	if q.Limit > 0 && q.Offset+q.Limit < end {
		end = q.Offset + q.Limit
	}
	out := append([]Record(nil), matched[q.Offset:end]...)
	return out, total, nil
}

func (m *Memory) CallIDByGenerationTask(_ context.Context, generationTaskID string) (string, error) {
	if m == nil || generationTaskID == "" {
		return "", ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.Records {
		if rec.GenerationTaskID == generationTaskID && rec.CallID != "" {
			return rec.CallID, nil
		}
	}
	return "", ErrNotFound
}

func (p *Postgres) CallIDByGenerationTask(ctx context.Context, generationTaskID string) (string, error) {
	if p == nil || p.client == nil || generationTaskID == "" {
		return "", ErrNotFound
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	row, err := p.client.ModelCall.Query().Where(modelcall.GenerationTaskIDEQ(generationTaskID)).Only(persistCtx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", ErrNotFound
		}
		return "", err
	}
	return row.ID, nil
}

func (m *Memory) Get(_ context.Context, callID string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rec := range m.Records {
		if rec.CallID == callID {
			return rec, nil
		}
	}
	return Record{}, ErrNotFound
}

func (p *Postgres) List(ctx context.Context, q Query) ([]Record, int, error) {
	query := p.client.ModelCall.Query().Where(predicates(q)...)
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := query.
		Order(modelcall.ByStartedAt(sql.OrderDesc()), modelcall.ByID(sql.OrderDesc())).
		Limit(q.Limit).
		Offset(q.Offset).
		All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromEnt(row))
	}
	return out, total, nil
}

func (p *Postgres) Get(ctx context.Context, callID string) (Record, error) {
	row, err := p.client.ModelCall.Get(ctx, callID)
	if err != nil {
		if isNotFound(err) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	return fromEnt(row), nil
}

func predicates(q Query) []predicate.ModelCall {
	out := make([]predicate.ModelCall, 0, 6)
	if !q.From.IsZero() {
		out = append(out, modelcall.StartedAtGTE(q.From))
	}
	if !q.To.IsZero() {
		out = append(out, modelcall.StartedAtLT(q.To))
	}
	if q.Model != "" {
		out = append(out, modelcall.ModelEQ(q.Model))
	}
	if q.Status != "" {
		out = append(out, modelcall.StatusEQ(q.Status))
	}
	if q.CallerService != "" {
		out = append(out, modelcall.CallerServiceEQ(q.CallerService))
	}
	if !q.IncludeEval {
		out = append(out, modelcall.BusinessSceneNEQ(EvalBusinessScene))
	}
	return out
}

func matchQuery(rec Record, q Query) bool {
	if !q.From.IsZero() && rec.StartedAt.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && !rec.StartedAt.Before(q.To) {
		return false
	}
	if q.Model != "" && rec.Model != q.Model {
		return false
	}
	if q.Status != "" && rec.Status != q.Status {
		return false
	}
	if q.CallerService != "" && rec.CallerService != q.CallerService {
		return false
	}
	if !q.IncludeEval && rec.BusinessScene == EvalBusinessScene {
		return false
	}
	return true
}

func sortByStartedDesc(records []Record) {
	for i := 1; i < len(records); i++ {
		item := records[i]
		j := i
		for j > 0 && (records[j-1].StartedAt.Before(item.StartedAt) || (records[j-1].StartedAt.Equal(item.StartedAt) && records[j-1].CallID < item.CallID)) {
			records[j] = records[j-1]
			j--
		}
		records[j] = item
	}
}

// Replayable 判断这条账本能不能换模型重放。媒体没归档、ASR 和向量类没有可重放输入。
func Replayable(rec Record) (bool, string) {
	switch rec.Operation {
	case OperationGenerateText, OperationGenerateImage, OperationGenerateVideo, OperationSubmitGeneration:
	default:
		return false, "这一期不能重放该操作"
	}
	if rec.Status != StatusSucceeded {
		return false, "只有成功的调用可以重放"
	}
	raw, _ := json.Marshal(rec.InputPayload)
	text := string(raw)
	if strings.Contains(text, "ledger://omitted") || strings.Contains(text, "ledger://pending") {
		return false, "输入媒体未归档"
	}
	switch rec.Capability {
	case CapabilityASR, "embedding", "rerank", "face_embedding", "face_library", "person_detect", "human_parser", "segment":
		return false, "这一期不重放该能力"
	}
	return true, ""
}

// Preview 从账本 JSON 抽出短文本，供列表展示。完整载荷只在单条详情返回。
func Preview(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	var b strings.Builder
	collectText(payload, &b)
	text := strings.TrimSpace(b.String())
	if text == "" {
		raw, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		text = string(raw)
	}
	if utf8.RuneCountInString(text) <= previewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:previewRunes]) + "…"
}

// FirstMediaKey 找第一条已归档对象键，供列表缩略图。未归档标记不当成可展示媒体。
func FirstMediaKey(payload map[string]any) string {
	var found string
	walk(payload, func(k string, v any) {
		if found != "" {
			return
		}
		s, ok := v.(string)
		if !ok || strings.HasPrefix(s, "ledger://") {
			return
		}
		if key := ObjectKeyFromURI(s); strings.HasPrefix(key, "model-calls/") {
			found = key
		}
	})
	return found
}

// ObjectKeyFromURI 从 s3://bucket/key 取出 key。已经是对象键时原样返回。
func ObjectKeyFromURI(uri string) string {
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "s3://") {
		rest := strings.TrimPrefix(uri, "s3://")
		_, key, ok := strings.Cut(rest, "/")
		if !ok {
			return ""
		}
		return key
	}
	return uri
}

func collectText(v any, b *strings.Builder) {
	switch t := v.(type) {
	case map[string]any:
		if text, ok := t["text"].(string); ok && strings.TrimSpace(text) != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(strings.TrimSpace(text))
		}
		for k, child := range t {
			if k == "text" {
				continue
			}
			collectText(child, b)
		}
	case []any:
		for _, child := range t {
			collectText(child, b)
		}
	}
}

func walk(v any, fn func(string, any)) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			fn(k, child)
			walk(child, fn)
		}
	case []any:
		for _, child := range t {
			walk(child, fn)
		}
	}
}

func isNotFound(err error) bool {
	return ent.IsNotFound(err)
}

// ErrNotFound 表示账本里没有这条 call_id。
var ErrNotFound = errors.New("model call not found")
