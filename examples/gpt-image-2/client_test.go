package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

type recordingService struct {
	modelhubv2.UnimplementedModelHubServiceServer
	events []*modelhubv2.GenerateEvent
	err    error
	got    *modelhubv2.GenerateRequest
}

func (service *recordingService) Generate(request *modelhubv2.GenerateRequest, stream modelhubv2.ModelHubService_GenerateServer) error {
	service.got = request
	for _, event := range service.events {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return service.err
}

func newTestClient(t *testing.T, service *recordingService) modelhubv2.ModelHubServiceClient {
	t.Helper()
	listener := bufconn.Listen(bufSize)
	server := grpc.NewServer(grpc.MaxRecvMsgSize(protocol.MaxRPCMessageBytes), grpc.MaxSendMsgSize(protocol.MaxRPCMessageBytes))
	modelhubv2.RegisterModelHubServiceServer(server, service)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return modelhubv2.NewModelHubServiceClient(conn)
}

func inlineImage(mimeType string, data []byte) *modelhubv2.OutputItem {
	return &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
		MimeType: mimeType,
		Source:   &modelhubv2.Media_Data{Data: data},
	}}}
}

func uriImage(mimeType, uri string) *modelhubv2.OutputItem {
	return &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{Image: &modelhubv2.Media{
		MimeType: mimeType,
		Source:   &modelhubv2.Media_Uri{Uri: uri},
	}}}
}

func textOutput(text string) *modelhubv2.OutputItem {
	return &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Text{Text: text}}
}

func videoOutput() *modelhubv2.OutputItem {
	return &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Video{Video: &modelhubv2.Media{
		MimeType: "video/mp4",
		Source:   &modelhubv2.Media_Data{Data: []byte("video-data")},
	}}}
}

func toolCallOutput() *modelhubv2.OutputItem {
	return &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_ToolCall{ToolCall: &modelhubv2.ToolCall{}}}
}

func TestGenerateImage(t *testing.T) {
	prompt := "a small red paper boat"
	imageData := []byte("png-data")

	t.Run("sends fixed request and returns a final inline image", func(t *testing.T) {
		service := &recordingService{events: []*modelhubv2.GenerateEvent{{
			Final: true,
			Items: []*modelhubv2.OutputItem{inlineImage("image/png", imageData)},
		}}}
		result, failure := generateImage(context.Background(), newTestClient(t, service), prompt)
		if failure != nil {
			t.Fatalf("generateImage failure=%v", failure)
		}
		if result.mimeType != "image/png" {
			t.Fatalf("mimeType=%q", result.mimeType)
		}
		if string(result.data) != string(imageData) {
			t.Fatalf("data=%q", result.data)
		}

		got := service.got
		if got.GetModel() != models.GPTImage2 {
			t.Fatalf("model=%q", got.GetModel())
		}
		message := got.GetInput().GetItems()[0].GetMessage()
		if message.GetRole() != modelhubv2.Role_ROLE_USER || message.GetParts()[0].GetText() != prompt {
			t.Fatalf("input=%#v", got.GetInput())
		}
		image := got.GetOutput().GetImage()
		if image == nil || image.GetAspectRatio() != "1:1" || image.GetImageSize() != "1024x1024" {
			t.Fatalf("output=%#v", got.GetOutput())
		}
	})

	t.Run("accepts image just below media limit", func(t *testing.T) {
		data := make([]byte, protocol.MaxMediaBytes-1)
		data[len(data)-1] = 1
		service := &recordingService{events: []*modelhubv2.GenerateEvent{{
			Final: true,
			Items: []*modelhubv2.OutputItem{inlineImage("image/png", data)},
		}}}
		result, failure := generateImage(context.Background(), newTestClient(t, service), prompt)
		if failure != nil {
			t.Fatalf("generateImage failure=%v", failure)
		}
		if result.mimeType != "image/png" || len(result.data) != len(data) || result.data[len(result.data)-1] != 1 {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("allows diagnostic text beside a valid image", func(t *testing.T) {
		service := &recordingService{events: []*modelhubv2.GenerateEvent{{
			Final: true,
			Items: []*modelhubv2.OutputItem{textOutput("diagnostic"), inlineImage("image/png", imageData)},
		}}}
		result, failure := generateImage(context.Background(), newTestClient(t, service), prompt)
		if failure != nil {
			t.Fatalf("generateImage failure=%v", failure)
		}
		if result.mimeType != "image/png" || string(result.data) != string(imageData) {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestGenerateImageRejectsInvalidStreams(t *testing.T) {
	validImage := inlineImage("image/png", []byte("png-data"))
	oversize := make([]byte, protocol.MaxMediaBytes+1)
	cases := []struct {
		name     string
		events   []*modelhubv2.GenerateEvent
		err      error
		category failureCategory
	}{
		{name: "missing-final", events: []*modelhubv2.GenerateEvent{{Items: []*modelhubv2.OutputItem{validImage}}}, category: failureProtocol},
		{name: "multiple-finals", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{validImage}}, {Final: true}}, category: failureProtocol},
		{name: "event-after-final", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{validImage}}, {Items: []*modelhubv2.OutputItem{textOutput("late")}}}, category: failureProtocol},
		{name: "missing-image", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{textOutput("only text")}}}, category: failureProtocol},
		{name: "multiple-images", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{validImage, inlineImage("image/png", []byte("second"))}}}, category: failureProtocol},
		{name: "uri-image", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{uriImage("image/png", "https://example.invalid/image.png")}}}, category: failureProtocol},
		{name: "missing-mime", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{inlineImage("", []byte("png-data"))}}}, category: failureProtocol},
		{name: "non-image-mime", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{inlineImage("text/plain", []byte("not an image"))}}}, category: failureProtocol},
		{name: "empty-data", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{inlineImage("image/png", nil)}}}, category: failureProtocol},
		{name: "oversize-data", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{inlineImage("image/png", oversize)}}}, category: failureProtocol},
		{name: "recv-error", err: status.Error(codes.Internal, "upstream body must not be exposed"), category: failureRPC},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "upstream body must not be exposed"), category: failureTimeout},
		{name: "diagnostic-only", events: []*modelhubv2.GenerateEvent{{Final: true, Items: []*modelhubv2.OutputItem{textOutput("blocked")}}}, category: failureProtocol},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := &recordingService{events: test.events, err: test.err}
			_, failure := generateImage(context.Background(), newTestClient(t, service), "prompt")
			if failure == nil {
				t.Fatal("generateImage succeeded")
			}
			if failure.category != test.category {
				t.Fatalf("category=%q, want %q", failure.category, test.category)
			}
			if test.category == failureRPC && failure.grpcCode != codes.Internal {
				t.Fatalf("grpcCode=%s", failure.grpcCode)
			}
			if test.category == failureTimeout && failure.grpcCode != codes.DeadlineExceeded {
				t.Fatalf("grpcCode=%s", failure.grpcCode)
			}
			if failure.Error() == "" {
				t.Fatalf("failure=%v", failure)
			}
		})
	}
}

func TestGenerateImageRejectsUnexpectedOutputItemsBeforeAndAfterImage(t *testing.T) {
	validImage := inlineImage("image/png", []byte("png-data"))
	unexpectedItems := []struct {
		name string
		item *modelhubv2.OutputItem
	}{
		{name: "video", item: videoOutput()},
		{name: "tool-call", item: toolCallOutput()},
		{name: "nil-item", item: nil},
		{name: "unknown-item", item: &modelhubv2.OutputItem{}},
		{name: "nil-image", item: &modelhubv2.OutputItem{Item: &modelhubv2.OutputItem_Image{}}},
	}

	for _, unexpected := range unexpectedItems {
		for _, position := range []string{"before-image", "after-image"} {
			t.Run(unexpected.name+"/"+position, func(t *testing.T) {
				items := []*modelhubv2.OutputItem{unexpected.item, validImage}
				if position == "after-image" {
					items[0], items[1] = items[1], items[0]
				}
				service := &recordingService{events: []*modelhubv2.GenerateEvent{{
					Final: true,
					Items: items,
				}}}

				_, failure := generateImage(context.Background(), newTestClient(t, service), "prompt")
				if failure == nil || failure.category != failureProtocol {
					t.Fatalf("failure=%v, want category=%q", failure, failureProtocol)
				}
			})
		}
	}
}

func TestWriteImage(t *testing.T) {
	data := []byte("png-data")

	t.Run("writes exact private file", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "image.png")
		if failure := writeImage(path, data, false); failure != nil {
			t.Fatalf("writeImage failure=%v", failure)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != string(data) {
			t.Fatalf("data=%q", got)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode=%#o", info.Mode().Perm())
		}
	})

	cases := []struct {
		name  string
		setup func(*testing.T) (string, bool, string)
	}{
		{
			name: "missing parent directory",
			setup: func(t *testing.T) (string, bool, string) {
				directory := filepath.Join(t.TempDir(), "missing")
				return filepath.Join(directory, "image.png"), false, directory
			},
		},
		{
			name: "existing destination without force",
			setup: func(t *testing.T) (string, bool, string) {
				directory := t.TempDir()
				path := filepath.Join(directory, "image.png")
				if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path, false, directory
			},
		},
		{
			name: "rename failure cleans temporary file",
			setup: func(t *testing.T) (string, bool, string) {
				directory := t.TempDir()
				path := filepath.Join(directory, "destination-directory")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return path, true, directory
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			path, force, directory := test.setup(t)
			failure := writeImage(path, data, force)
			if failure == nil || failure.category != failureOutput {
				t.Fatalf("failure=%v", failure)
			}
			matches, err := filepath.Glob(filepath.Join(directory, ".image.png.tmp-*"))
			if err != nil {
				t.Fatalf("glob: %v", err)
			}
			if len(matches) != 0 {
				t.Fatalf("temporary files left behind: %v", matches)
			}
		})
	}

	t.Run("overwrites only with force", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "image.png")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if failure := writeImage(path, data, true); failure != nil {
			t.Fatalf("writeImage failure=%v", failure)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(data) {
			t.Fatalf("data=%q", got)
		}
	})

	t.Run("does not overwrite destination created before non-force publication", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "image.png")
		concurrentData := []byte("concurrent destination")
		originalLinkFile := linkFile
		linkFile = func(oldname, newname string) error {
			if err := os.WriteFile(newname, concurrentData, 0o600); err != nil {
				return err
			}
			return originalLinkFile(oldname, newname)
		}
		t.Cleanup(func() { linkFile = originalLinkFile })

		failure := writeImage(path, data, false)
		if failure == nil || failure.category != failureOutput {
			t.Fatalf("failure=%v", failure)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != string(concurrentData) {
			t.Fatalf("data=%q, want %q", got, concurrentData)
		}
		matches, err := filepath.Glob(filepath.Join(directory, ".image.png.tmp-*"))
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary files left behind: %v", matches)
		}
	})

	t.Run("write failure cleans temporary file", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "image.png")
		originalCreateTempFile := createTempFile
		createTempFile = func(directory, pattern string) (*os.File, error) {
			temporary, err := os.CreateTemp(directory, pattern)
			if err != nil {
				return nil, err
			}
			name := temporary.Name()
			if err := temporary.Close(); err != nil {
				return nil, err
			}
			return os.Open(name)
		}
		t.Cleanup(func() { createTempFile = originalCreateTempFile })

		failure := writeImage(path, data, false)
		if failure == nil || failure.category != failureOutput {
			t.Fatalf("failure=%v", failure)
		}
		matches, err := filepath.Glob(filepath.Join(directory, ".image.png.tmp-*"))
		if err != nil {
			t.Fatalf("glob: %v", err)
		}
		if len(matches) != 0 {
			t.Fatalf("temporary files left behind: %v", matches)
		}
	})
}

func TestSmokeFailureErrorIsSanitized(t *testing.T) {
	failure := &smokeFailure{category: failureRPC, grpcCode: codes.Internal}
	if got, want := failure.Error(), "gpt-image-2 failed: category=rpc grpc_code=Internal"; got != want {
		t.Fatalf("Error()=%q, want %q", got, want)
	}
	failure = &smokeFailure{category: failureProtocol}
	if got, want := failure.Error(), "gpt-image-2 failed: category=protocol"; got != want {
		t.Fatalf("Error()=%q, want %q", got, want)
	}
}
