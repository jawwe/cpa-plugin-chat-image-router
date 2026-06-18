package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type imageRequestPlan struct {
	Model          string
	Prompt         string
	Images         []string
	ResponseFormat string
	Endpoint       string
	ImageRequest   []byte
	ToolRequest    []byte
}

func buildImageRequestFromChat(raw []byte, cfg pluginConfig) (imageRequestPlan, error) {
	if !json.Valid(raw) {
		return imageRequestPlan{}, fmt.Errorf("chat request body must be valid JSON")
	}
	root := gjson.ParseBytes(raw)
	model := strings.TrimSpace(root.Get("model").String())
	if model == "" {
		return imageRequestPlan{}, fmt.Errorf("model is required")
	}
	if !cfg.matchesModel(model) {
		return imageRequestPlan{}, fmt.Errorf("model %s is not configured for chat image routing", model)
	}

	prompt, images := extractPromptAndImages(root)
	if prompt == "" {
		prompt = strings.TrimSpace(root.Get("prompt").String())
	}
	if prompt == "" {
		return imageRequestPlan{}, fmt.Errorf("image prompt is required")
	}
	images = append(images, collectTopLevelImages(root)...)
	images = dedupeTrimmed(images)

	responseFormat := normalizeResponseFormat(root.Get("response_format").String())
	if !root.Get("response_format").Exists() {
		responseFormat = cfg.ResponseFormat
	}

	imageReq := []byte(`{}`)
	imageReq, _ = sjson.SetBytes(imageReq, "model", model)
	imageReq, _ = sjson.SetBytes(imageReq, "prompt", prompt)
	imageReq, _ = sjson.SetBytes(imageReq, "response_format", responseFormat)
	copyImageFields(raw, &imageReq, []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation", "aspect_ratio", "resolution"})
	copyImageNumberFields(raw, &imageReq, []string{"output_compression", "partial_images", "n"})

	endpoint := "/v1/images/generations"
	action := "generate"
	if len(images) > 0 {
		endpoint = "/v1/images/edits"
		action = "edit"
		if isGPTImage2Model(model) {
			for _, img := range images {
				item := []byte(`{"image_url":""}`)
				item, _ = sjson.SetBytes(item, "image_url", img)
				imageReq, _ = sjson.SetRawBytes(imageReq, "images.-1", item)
			}
		} else if len(images) == 1 {
			imageReq, _ = sjson.SetBytes(imageReq, "image", images[0])
		} else {
			for _, img := range images {
				imageReq, _ = sjson.SetBytes(imageReq, "images.-1", img)
			}
		}
	}

	toolReq := []byte(`{"type":"image_generation"}`)
	toolReq, _ = sjson.SetBytes(toolReq, "action", action)
	toolReq, _ = sjson.SetBytes(toolReq, "model", model)
	copyImageFields(raw, &toolReq, []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"})
	copyImageNumberFields(raw, &toolReq, []string{"output_compression", "partial_images"})
	if mask := strings.TrimSpace(root.Get("mask.image_url").String()); mask != "" {
		toolReq, _ = sjson.SetBytes(toolReq, "input_image_mask.image_url", mask)
	}

	return imageRequestPlan{
		Model:          model,
		Prompt:         prompt,
		Images:         images,
		ResponseFormat: responseFormat,
		Endpoint:       endpoint,
		ImageRequest:   imageReq,
		ToolRequest:    toolReq,
	}, nil
}

func extractPromptAndImages(root gjson.Result) (string, []string) {
	var textParts []string
	var images []string
	messages := root.Get("messages")
	if !messages.IsArray() {
		return "", nil
	}
	for _, msg := range messages.Array() {
		role := strings.TrimSpace(msg.Get("role").String())
		if strings.EqualFold(role, "assistant") || strings.EqualFold(role, "tool") {
			continue
		}
		texts, imgs := extractContent(msg.Get("content"))
		textParts = append(textParts, texts...)
		images = append(images, imgs...)
	}
	return strings.TrimSpace(strings.Join(textParts, "\n")), images
}

func extractContent(content gjson.Result) ([]string, []string) {
	if content.Type == gjson.String {
		text := strings.TrimSpace(content.String())
		if text == "" {
			return nil, nil
		}
		return []string{text}, nil
	}
	if !content.IsArray() {
		return nil, nil
	}
	var texts []string
	var images []string
	for _, part := range content.Array() {
		if part.Type == gjson.String {
			if text := strings.TrimSpace(part.String()); text != "" {
				texts = append(texts, text)
			}
			continue
		}
		if text := strings.TrimSpace(part.Get("text").String()); text != "" {
			texts = append(texts, text)
		}
		for _, path := range []string{"image_url.url", "image_url", "input_image.image_url", "image_url_value", "url"} {
			if image := strings.TrimSpace(part.Get(path).String()); image != "" {
				images = append(images, image)
				break
			}
		}
	}
	return texts, images
}

func collectTopLevelImages(root gjson.Result) []string {
	var images []string
	appendImage := func(value string) {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			images = append(images, trimmed)
		}
	}
	for _, path := range []string{"image", "image_url", "input_image"} {
		value := root.Get(path)
		if value.Type == gjson.String {
			appendImage(value.String())
		} else if value.IsObject() {
			appendImage(value.Get("image_url.url").String())
			appendImage(value.Get("image_url").String())
			appendImage(value.Get("url").String())
		}
	}
	for _, path := range []string{"images", "input_images"} {
		value := root.Get(path)
		if !value.IsArray() {
			continue
		}
		for _, item := range value.Array() {
			if item.Type == gjson.String {
				appendImage(item.String())
				continue
			}
			appendImage(item.Get("image_url.url").String())
			appendImage(item.Get("image_url").String())
			appendImage(item.Get("url").String())
		}
	}
	return images
}

func copyImageFields(raw []byte, target *[]byte, fields []string) {
	for _, field := range fields {
		if value := strings.TrimSpace(gjson.GetBytes(raw, field).String()); value != "" {
			*target, _ = sjson.SetBytes(*target, field, value)
		}
	}
}

func copyImageNumberFields(raw []byte, target *[]byte, fields []string) {
	for _, field := range fields {
		value := gjson.GetBytes(raw, field)
		if value.Exists() && value.Type == gjson.Number {
			*target, _ = sjson.SetBytes(*target, field, value.Int())
		}
	}
}

func dedupeTrimmed(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func buildImagesResponsesRequest(prompt string, images []string, toolJSON []byte, mainModel string) []byte {
	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	req, _ = sjson.SetBytes(req, "model", strings.TrimSpace(mainModel))

	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	contentIndex := 1
	for _, img := range images {
		if strings.TrimSpace(img) == "" {
			continue
		}
		part := []byte(`{"type":"input_image","image_url":""}`)
		part, _ = sjson.SetBytes(part, "image_url", strings.TrimSpace(img))
		path := fmt.Sprintf("0.content.%d", contentIndex)
		input, _ = sjson.SetRawBytes(input, path, part)
		contentIndex++
	}
	req, _ = sjson.SetRawBytes(req, "input", input)
	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		req, _ = sjson.SetRawBytes(req, "tools.-1", toolJSON)
	}
	return req
}

func isGPTImage2Model(model string) bool {
	return strings.EqualFold(imageModelBase(model), defaultImagesToolModel)
}

func responseMainModel(imageModel string, configuredMainModel string) string {
	mainModel := strings.TrimSpace(configuredMainModel)
	if mainModel == "" {
		mainModel = defaultImagesMainModel
	}
	imageModel = strings.TrimSpace(imageModel)
	if idx := strings.LastIndex(imageModel, "/"); idx > 0 && idx < len(imageModel)-1 {
		prefix := strings.TrimSpace(imageModel[:idx])
		if prefix != "" && !strings.Contains(mainModel, "/") {
			return prefix + "/" + mainModel
		}
	}
	return mainModel
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if errIndent := json.Indent(&buf, raw, "", "  "); errIndent != nil {
		return string(raw)
	}
	return buf.String()
}
