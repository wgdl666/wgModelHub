package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
	"github.com/wgdl666/wgModelHub/models"
	"github.com/wgdl666/wgModelHub/protocol"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type failureCategory string

const (
	failureSourceValidation failureCategory = "source-validation"
	failureConnect          failureCategory = "connect"
	failureRPC              failureCategory = "rpc"
	failureTimeout          failureCategory = "timeout"
	failureProtocol         failureCategory = "protocol"
	failureOutput           failureCategory = "output"
)

type smokeFailure struct {
	category failureCategory
	grpcCode codes.Code
}

func (failure *smokeFailure) Error() string {
	if failure.grpcCode != codes.OK {
		return fmt.Sprintf("gpt-image-2 failed: category=%s grpc_code=%s", failure.category, failure.grpcCode)
	}
	return fmt.Sprintf("gpt-image-2 failed: category=%s", failure.category)
}

type imageResult struct {
	mimeType string
	data     []byte
}

var createTempFile = os.CreateTemp
var linkFile = os.Link

func generateImage(ctx context.Context, client modelhubv2.ModelHubServiceClient, prompt string) (imageResult, *smokeFailure) {
	request := &modelhubv2.GenerateRequest{
		Model: models.GPTImage2,
		Input: &modelhubv2.Input{Items: []*modelhubv2.InputItem{{
			Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{
				Role: modelhubv2.Role_ROLE_USER,
				Parts: []*modelhubv2.ContentPart{{
					Content: &modelhubv2.ContentPart_Text{Text: prompt},
				}},
			}},
		}}},
		Output: &modelhubv2.OutputSpec{Kind: &modelhubv2.OutputSpec_Image{
			Image: &modelhubv2.ImageOutput{
				AspectRatio: proto.String("1:1"),
				ImageSize:   proto.String("1024x1024"),
			},
		}},
	}

	stream, err := client.Generate(ctx, request, grpc.MaxCallRecvMsgSize(protocol.MaxRPCMessageBytes))
	if err != nil {
		return imageResult{}, rpcFailure(err)
	}

	var result imageResult
	finalSeen := false
	imageCount := 0
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return imageResult{}, rpcFailure(err)
		}
		if finalSeen {
			return imageResult{}, &smokeFailure{category: failureProtocol}
		}
		if event.GetFinal() {
			finalSeen = true
		}
		for _, item := range event.GetItems() {
			if item == nil {
				return imageResult{}, &smokeFailure{category: failureProtocol}
			}
			var image *modelhubv2.Media
			switch output := item.GetItem().(type) {
			case *modelhubv2.OutputItem_Text:
				continue
			case *modelhubv2.OutputItem_Image:
				image = output.Image
			default:
				return imageResult{}, &smokeFailure{category: failureProtocol}
			}
			if image == nil {
				return imageResult{}, &smokeFailure{category: failureProtocol}
			}
			imageCount++
			if imageCount > 1 {
				return imageResult{}, &smokeFailure{category: failureProtocol}
			}
			dataSource, ok := image.GetSource().(*modelhubv2.Media_Data)
			if !ok || !isSafeImageMIME(image.GetMimeType()) || len(dataSource.Data) == 0 || len(dataSource.Data) > protocol.MaxMediaBytes {
				return imageResult{}, &smokeFailure{category: failureProtocol}
			}
			result = imageResult{mimeType: image.GetMimeType(), data: dataSource.Data}
		}
	}
	if !finalSeen || imageCount != 1 {
		return imageResult{}, &smokeFailure{category: failureProtocol}
	}
	return result, nil
}

func isSafeImageMIME(mimeType string) bool {
	const prefix = "image/"
	const tokenCharacters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&'*+.^_`|~-"
	if !strings.HasPrefix(mimeType, prefix) || len(mimeType) == len(prefix) {
		return false
	}
	for index := len(prefix); index < len(mimeType); index++ {
		if !strings.ContainsRune(tokenCharacters, rune(mimeType[index])) {
			return false
		}
	}
	return true
}

func rpcFailure(err error) *smokeFailure {
	code := status.Code(err)
	if code == codes.DeadlineExceeded {
		return &smokeFailure{category: failureTimeout, grpcCode: code}
	}
	return &smokeFailure{category: failureRPC, grpcCode: code}
}

func writeImage(path string, data []byte, force bool) *smokeFailure {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return &smokeFailure{category: failureOutput}
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			return &smokeFailure{category: failureOutput}
		}
	} else if !os.IsNotExist(err) {
		return &smokeFailure{category: failureOutput}
	}

	temporary, err := createTempFile(parent, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return &smokeFailure{category: failureOutput}
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return &smokeFailure{category: failureOutput}
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		return &smokeFailure{category: failureOutput}
	}
	if err := temporary.Sync(); err != nil {
		return &smokeFailure{category: failureOutput}
	}
	if err := temporary.Close(); err != nil {
		return &smokeFailure{category: failureOutput}
	}
	if force {
		if err := os.Rename(temporary.Name(), path); err != nil {
			return &smokeFailure{category: failureOutput}
		}
		renamed = true
		return nil
	}
	if err := linkFile(temporary.Name(), path); err != nil {
		return &smokeFailure{category: failureOutput}
	}
	if err := os.Remove(temporary.Name()); err != nil {
		return &smokeFailure{category: failureOutput}
	}
	renamed = true
	return nil
}
