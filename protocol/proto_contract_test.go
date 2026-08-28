package protocol_test

import (
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSynthesizeSpeechProtoContract(t *testing.T) {
	desc := modelhubv2.File_proto_wg_model_hub_v2_model_hub_proto.Services().ByName("ModelHubService")
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
	if req.Fields().Len() != 3 {
		t.Fatalf("request fields=%d", req.Fields().Len())
	}

	resp := (&modelhubv2.SynthesizeSpeechResponse{}).ProtoReflect().Descriptor()
	audio := resp.Fields().ByNumber(1)
	if audio == nil || audio.Name() != "audio" || audio.Message().FullName() != "wg_model_hub.v2.Media" {
		t.Fatalf("audio field=%v", audio)
	}
}

func assertField(t *testing.T, desc protoreflect.MessageDescriptor, number protoreflect.FieldNumber, name protoreflect.Name, kind protoreflect.Kind) {
	t.Helper()
	field := desc.Fields().ByNumber(number)
	if field == nil || field.Name() != name || field.Kind() != kind {
		t.Fatalf("field #%d want %s/%v got %v", number, name, kind, field)
	}
}
