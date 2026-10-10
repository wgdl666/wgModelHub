package modelhub

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/internal/callledger"
)

const (
	listDefaultLimit = 20
	listMaxLimit     = 100
)

// ListModelCalls 按时间倒序给出评测可选的调用。默认去掉评测重放自己，避免列表被对照结果淹没。
func (s *Service) ListModelCalls(ctx context.Context, request *modelhubv2.ListModelCallsRequest) (*modelhubv2.ListModelCallsResponse, error) {
	reader, err := s.callReader()
	if err != nil {
		return nil, err
	}
	if request == nil {
		request = &modelhubv2.ListModelCallsRequest{}
	}
	limit := int(request.GetLimit())
	if limit <= 0 {
		limit = listDefaultLimit
	}
	if limit > listMaxLimit {
		limit = listMaxLimit
	}
	offset := int(request.GetOffset())
	if offset < 0 {
		offset = 0
	}
	q := callledger.Query{
		Model:         strings.TrimSpace(request.GetModel()),
		Status:        strings.TrimSpace(request.GetStatus()),
		CallerService: strings.TrimSpace(request.GetCallerService()),
		IncludeEval:   request.GetIncludeEval(),
		Limit:         limit,
		Offset:        offset,
	}
	if request.GetFrom() != nil {
		q.From = request.GetFrom().AsTime()
	}
	if request.GetTo() != nil {
		q.To = request.GetTo().AsTime()
	}
	records, total, err := reader.List(ctx, q)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list model calls: %v", err)
	}
	out := make([]*modelhubv2.ModelCallSummary, 0, len(records))
	for _, rec := range records {
		out = append(out, summaryOf(rec))
	}
	if total > int(^uint32(0)>>1) {
		total = int(^uint32(0) >> 1)
	}
	return &modelhubv2.ListModelCallsResponse{Calls: out, Total: int32(total)}, nil
}

// GetModelCall 返回完整输入输出。列表不带载荷，对照页才按 call_id 取这一份。
func (s *Service) GetModelCall(ctx context.Context, request *modelhubv2.GetModelCallRequest) (*modelhubv2.GetModelCallResponse, error) {
	reader, err := s.callReader()
	if err != nil {
		return nil, err
	}
	callID := ""
	if request != nil {
		callID = strings.TrimSpace(request.GetCallId())
	}
	if callID == "" {
		return nil, status.Error(codes.InvalidArgument, "call_id is required")
	}
	rec, err := reader.Get(ctx, callID)
	if errors.Is(err, callledger.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "model call not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get model call: %v", err)
	}
	input, err := payloadStruct(rec.InputPayload)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode input: %v", err)
	}
	output, err := payloadStruct(rec.OutputPayload)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode output: %v", err)
	}
	return &modelhubv2.GetModelCallResponse{
		Summary:          summaryOf(rec),
		Input:            input,
		Output:           output,
		BusinessLine:     rec.BusinessLine,
		BusinessSubscene: rec.BusinessSubscene,
		Operation:        rec.Operation,
		LatencyMs:        rec.LatencyMS,
	}, nil
}

// GetModelCallMedia 只允许读取这条调用自己的对象键。评测页经这里拿图，不把桶凭据交给浏览器。
func (s *Service) GetModelCallMedia(ctx context.Context, request *modelhubv2.GetModelCallMediaRequest) (*modelhubv2.GetModelCallMediaResponse, error) {
	callID := ""
	key := ""
	if request != nil {
		callID = strings.TrimSpace(request.GetCallId())
		key = callledger.ObjectKeyFromURI(request.GetObjectKey())
	}
	if callID == "" || key == "" {
		return nil, status.Error(codes.InvalidArgument, "call_id and object_key are required")
	}
	if !mediaKeyAllowed(callID, key) {
		return nil, status.Error(codes.PermissionDenied, "object key is outside this call")
	}
	getter, ok := s.objects.(callledger.ObjectGetter)
	if !ok || getter == nil {
		return nil, status.Error(codes.FailedPrecondition, "object storage is not readable")
	}
	contentType, body, err := getter.Get(ctx, key)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "read media: %v", err)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return &modelhubv2.GetModelCallMediaResponse{MimeType: contentType, Data: body}, nil
}

func (s *Service) callReader() (callledger.Reader, error) {
	if s == nil || s.ledger == nil {
		return nil, status.Error(codes.Unavailable, "model call ledger is not configured")
	}
	reader, ok := s.ledger.(callledger.Reader)
	if !ok {
		return nil, status.Error(codes.Unavailable, "model call ledger is not readable")
	}
	return reader, nil
}

func summaryOf(rec callledger.Record) *modelhubv2.ModelCallSummary {
	ok, reason := callledger.Replayable(rec)
	out := &modelhubv2.ModelCallSummary{
		CallId:            rec.CallID,
		CallerService:     rec.CallerService,
		BusinessScene:     rec.BusinessScene,
		Model:             rec.Model,
		Capability:        rec.Capability,
		Status:            rec.Status,
		Replayable:        ok,
		ReplayBlockReason: reason,
		InputPreview:      callledger.Preview(rec.InputPayload),
		OutputPreview:     callledger.Preview(rec.OutputPayload),
		InputMediaKey:     callledger.FirstMediaKey(rec.InputPayload),
		OutputMediaKey:    callledger.FirstMediaKey(rec.OutputPayload),
	}
	if !rec.StartedAt.IsZero() {
		out.StartedAt = timestamppb.New(rec.StartedAt)
	}
	return out
}

func payloadStruct(payload map[string]any) (*structpb.Struct, error) {
	if len(payload) == 0 {
		return &structpb.Struct{}, nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return structpb.NewStruct(generic)
}

func mediaKeyAllowed(callID, key string) bool {
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	prefix := "model-calls/" + callID + "/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	if strings.Contains(key, "..") || strings.Contains(key, "//") {
		return false
	}
	return true
}
