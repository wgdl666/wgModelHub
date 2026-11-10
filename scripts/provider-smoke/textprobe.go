package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"
)

const recordSchema = `{"type":"object","properties":{"code":{"type":"string","enum":["OK"]}},"required":["code"],"additionalProperties":false}`

func textProbeRequest(model, mode string, imageData []byte) *modelhubv2.GenerateRequest {
	prompt := "只回复 OK，不要添加其他内容。输入示例包含引号和符号：\"<&>\"。"
	if strings.HasPrefix(mode, "vision") {
		prompt = "Name one visible clothing item. Answer in a short phrase."
	}
	if mode == "tools" {
		prompt = "Call record_result with code equal to OK. Do not reply with plain text."
	}
	if mode == "schema" {
		prompt = "Return a JSON object with code equal to OK."
	}
	parts := []*modelhubv2.ContentPart{{Content: &modelhubv2.ContentPart_Text{Text: prompt}}}
	if strings.HasPrefix(mode, "vision") {
		parts = append(parts, &modelhubv2.ContentPart{Content: &modelhubv2.ContentPart_Image{Image: &modelhubv2.Media{MimeType: http.DetectContentType(imageData), Source: &modelhubv2.Media_Data{Data: imageData}}}})
	}
	limit := int32(1024)
	spec := &modelhubv2.TextOutput{MaxOutputTokens: &limit}
	input := &modelhubv2.Input{Items: []*modelhubv2.InputItem{{Item: &modelhubv2.InputItem_Message{Message: &modelhubv2.Message{Role: modelhubv2.Role_ROLE_USER, Parts: parts}}}}}
	if mode == "tools" {
		input.Tools = []*modelhubv2.Tool{{Function: &modelhubv2.FunctionDefinition{Name: "record_result", Description: "Return the diagnostic code. 示例：\"<&>\"。", ParametersJsonSchema: []byte(recordSchema)}}}
		input.ToolChoice = &modelhubv2.ToolChoice{Mode: modelhubv2.ToolChoiceMode_TOOL_CHOICE_MODE_FUNCTION, FunctionName: "record_result"}
	}
	if mode == "schema" {
		spec.ResponseFormat = &modelhubv2.ResponseFormat{Type: modelhubv2.ResponseFormatType_RESPONSE_FORMAT_TYPE_JSON_SCHEMA, Name: "smoke_record", JsonSchema: []byte(recordSchema)}
	}
	return &modelhubv2.GenerateRequest{Model: model, Input: input, Output: &modelhubv2.OutputSpec{Stream: strings.HasSuffix(mode, "-stream"), Kind: &modelhubv2.OutputSpec_Text{Text: spec}}}
}

func validateTextProbe(mode, text string, tools []*modelhubv2.ToolCall) error {
	if mode == "text" || mode == "text-stream" {
		if strings.TrimSpace(text) != "OK" {
			return fmt.Errorf("expected OK text response")
		}
	}
	var data []byte
	if mode == "schema" {
		data = []byte(text)
	}
	if mode == "tools" {
		if len(tools) != 1 || tools[0].GetName() != "record_result" || tools[0].GetId() == "" {
			return fmt.Errorf("unexpected tool call")
		}
		data = tools[0].GetArgumentsJson()
	}
	if mode == "schema" || mode == "tools" {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if len(value) != 1 || value["code"] != "OK" {
			return fmt.Errorf("unexpected structured response")
		}
	}
	return nil
}
