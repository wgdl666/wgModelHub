package protocol_test

import (
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// 协议测试使用当前生成代码的文件描述符；protoc 的导入根目录不带 proto/ 前缀。
func TestListModelsProtoContract(t *testing.T) {
	desc := modelhubv2.File_wg_model_hub_v2_model_hub_proto.Services().ByName("ModelHubService")
	if desc == nil {
		t.Fatal("ModelHubService missing")
	}
	method := desc.Methods().ByName("ListModels")
	if method == nil {
		t.Fatal("ListModels RPC missing")
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		t.Fatal("ListModels must be unary")
	}
	if method.Input().FullName() != "wg_model_hub.v2.ListModelsRequest" {
		t.Fatalf("input=%s", method.Input().FullName())
	}
	if method.Output().FullName() != "wg_model_hub.v2.ListModelsResponse" {
		t.Fatalf("output=%s", method.Output().FullName())
	}

	req := (&modelhubv2.ListModelsRequest{}).ProtoReflect().Descriptor()
	category := req.Fields().ByNumber(1)
	if category == nil || category.Name() != "category" || category.Enum() == nil {
		t.Fatalf("category field=%v", category)
	}
	if req.Fields().Len() != 1 {
		t.Fatalf("request fields=%d", req.Fields().Len())
	}

	info := (&modelhubv2.ModelInfo{}).ProtoReflect().Descriptor()
	assertField(t, info, 1, "model", protoreflect.StringKind)
	infoCategory := info.Fields().ByNumber(2)
	if infoCategory == nil || infoCategory.Name() != "category" || infoCategory.Enum() == nil {
		t.Fatalf("model category field=%v", infoCategory)
	}
}

func TestSynthesizeSpeechProtoContract(t *testing.T) {
	desc := modelhubv2.File_wg_model_hub_v2_model_hub_proto.Services().ByName("ModelHubService")
	if desc == nil {
		t.Fatal("ModelHubService missing")
	}
	method := desc.Methods().ByName("SynthesizeSpeech")
	if method == nil {
		t.Fatal("SynthesizeSpeech RPC missing")
	}
	if method.IsStreamingClient() || method.IsStreamingServer() {
		t.Fatal("SynthesizeSpeech must be unary")
	}
	if method.Input().FullName() != "wg_model_hub.v2.SynthesizeSpeechRequest" {
		t.Fatalf("input=%s", method.Input().FullName())
	}
	if method.Output().FullName() != "wg_model_hub.v2.SynthesizeSpeechResponse" {
		t.Fatalf("output=%s", method.Output().FullName())
	}

	req := (&modelhubv2.SynthesizeSpeechRequest{}).ProtoReflect().Descriptor()
	assertField(t, req, 1, "model", protoreflect.StringKind)
	assertField(t, req, 2, "text", protoreflect.StringKind)
	assertField(t, req, 3, "voice_id", protoreflect.StringKind)
	assertField(t, req, 4, "business_metadata", protoreflect.MessageKind)
	if req.Fields().Len() != 4 {
		t.Fatalf("request fields=%d", req.Fields().Len())
	}

	resp := (&modelhubv2.SynthesizeSpeechResponse{}).ProtoReflect().Descriptor()
	audio := resp.Fields().ByNumber(1)
	if audio == nil || audio.Name() != "audio" || audio.Message().FullName() != "wg_model_hub.v2.Media" {
		t.Fatalf("audio field=%v", audio)
	}
}

func TestPrefixCacheIsManagedInsideModelHub(t *testing.T) {
	file := modelhubv2.File_wg_model_hub_v2_model_hub_proto
	service := file.Services().ByName("ModelHubService")
	if service.Methods().ByName("CreateCachedContent") != nil {
		t.Fatal("caller-owned cache creation RPC must not be exposed")
	}
	for _, name := range []protoreflect.Name{"CreateCachedContentRequest", "CreateCachedContentResponse"} {
		if file.Messages().ByName(name) != nil {
			t.Fatalf("caller-owned cache message %s still exposed", name)
		}
	}
	input := (&modelhubv2.Input{}).ProtoReflect().Descriptor()
	if input.Fields().ByName("cached_content") != nil || input.Fields().ByNumber(6) != nil {
		t.Fatal("callers must send complete input instead of cache resource names")
	}
	if !input.ReservedRanges().Has(6) || !input.ReservedNames().Has("cached_content") {
		t.Fatal("removed cache field number and name must stay reserved")
	}
}

func assertField(t *testing.T, desc protoreflect.MessageDescriptor, number protoreflect.FieldNumber, name protoreflect.Name, kind protoreflect.Kind) {
	t.Helper()
	field := desc.Fields().ByNumber(number)
	if field == nil || field.Name() != name || field.Kind() != kind {
		t.Fatalf("field #%d want %s/%v got %v", number, name, kind, field)
	}
}
