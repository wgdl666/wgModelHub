package callledger

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const uploadTimeout = 30 * time.Second

// ObjectStore 保存图片和视频字节。表里只留 URI。
type ObjectStore interface {
	Put(ctx context.Context, key, contentType string, body []byte) (string, error)
}

// ObjectGetter 读取已归档媒体。和 Put 分开，避免只负责上传的测试替身必须实现读取。
type ObjectGetter interface {
	Get(ctx context.Context, key string) (contentType string, body []byte, err error)
}

// Blob 是一次调用里需要上传的内联图片或视频。
type Blob struct {
	Placeholder string
	MIME        string
	Data        []byte
}

// Call 把一次模型调用拆成两段账本写入：先记请求，返回后再记结果。
type Call struct {
	store   Store
	objects ObjectStore
	snap    Record
	blobs   []Blob
	done    chan struct{}
	abort   atomic.Bool
	goFn    func(func())
}

// TraceIDFromContext 读取当前 span 的 trace。无效 span 返回空字符串。
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// Open 异步写入请求，调用方不必等它完成再去打模型。
func Open(ctx context.Context, store Store, objects ObjectStore, rec *Record, blobs []Blob, goFn func(func())) *Call {
	if store == nil || rec == nil {
		return nil
	}
	if goFn == nil {
		goFn = func(fn func()) { go fn() }
	}
	snap := *rec
	if snap.Status == "" {
		snap.Status = StatusPending
	}
	snap.OutputPayload = nil
	snap.Usage = nil
	snap.UsageDetail = nil
	call := &Call{
		store:   store,
		objects: objects,
		snap:    snap,
		blobs:   blobs,
		done:    make(chan struct{}),
		goFn:    goFn,
	}
	goFn(func() { call.insert(ctx) })
	return call
}

func (c *Call) insert(ctx context.Context) {
	defer close(c.done)
	if c == nil || c.abort.Load() {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadTimeout)
	defer cancel()
	c.snap.InputPayload = AttachBlobs(persistCtx, c.objects, c.snap.CallID, c.snap.InputPayload, c.blobs)
	if c.abort.Load() {
		return
	}
	if err := c.store.Record(persistCtx, c.snap); err != nil {
		slog.Warn("model_call request persist failed", "call_id", c.snap.CallID, "err", err)
	}
}

// Finish 在结果已经返回之后异步更新同一行。
func (c *Call) Finish(rec *Record, blobs []Blob) {
	if c == nil || rec == nil {
		return
	}
	final := *rec
	c.goFn(func() {
		<-c.done
		persistCtx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
		defer cancel()
		final.OutputPayload = AttachBlobs(persistCtx, c.objects, final.CallID, final.OutputPayload, blobs)
		if err := c.store.UpdateResult(persistCtx, final); err != nil {
			slog.Warn("model_call result persist failed", "call_id", final.CallID, "err", err)
		}
	})
}

// Abort 用于供应商实际没被调用的路径。已经写入的请求行删掉，避免账本里留下未发生的调用。
func (c *Call) Abort() {
	if c == nil {
		return
	}
	c.abort.Store(true)
	c.goFn(func() {
		<-c.done
		persistCtx, cancel := context.WithTimeout(context.Background(), persistTimeout)
		defer cancel()
		if err := c.store.Delete(persistCtx, c.snap.CallID); err != nil {
			slog.Warn("model_call abort delete failed", "call_id", c.snap.CallID, "err", err)
		}
	})
}

func AttachBlobs(ctx context.Context, objects ObjectStore, callID string, payload map[string]any, blobs []Blob) map[string]any {
	if len(blobs) == 0 {
		return payload
	}
	if payload == nil {
		payload = map[string]any{}
	}
	var uris []any
	for _, blob := range blobs {
		uri := "ledger://omitted"
		if objects != nil && len(blob.Data) > 0 {
			key := objectKey(callID, blob.Placeholder)
			uploaded, err := objects.Put(ctx, key, blob.MIME, blob.Data)
			if err != nil {
				slog.Warn("model_call media upload failed", "call_id", callID, "key", key, "err", err)
			} else if uploaded != "" {
				uri = uploaded
			}
		}
		replaceString(payload, blob.Placeholder, uri)
		uris = append(uris, uri)
	}
	if len(uris) > 0 {
		payload["media_uris"] = uris
	}
	return payload
}

func objectKey(callID, placeholder string) string {
	suffix := strings.TrimPrefix(placeholder, "ledger://pending/")
	suffix = strings.Trim(suffix, "/")
	if suffix == "" {
		suffix = "media"
	}
	return "model-calls/" + callID + "/" + suffix
}

func replaceString(value any, from, to string) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if text, ok := child.(string); ok && text == from {
				node[key] = to
				continue
			}
			replaceString(child, from, to)
		}
	case []any:
		for i, child := range node {
			if text, ok := child.(string); ok && text == from {
				node[i] = to
				continue
			}
			replaceString(child, from, to)
		}
	}
}
