package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type xaiImageResult struct {
	B64JSON       string
	URL           string
	RevisedPrompt string
	MimeType      string
}

type imageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

func buildChatCompletionResponse(model string, imageBody []byte, _ string) ([]byte, error) {
	if !json.Valid(imageBody) {
		return nil, fmt.Errorf("image response is not valid JSON")
	}
	content, errContent := chatContentFromImagesResponse(imageBody)
	if errContent != nil {
		return nil, errContent
	}
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	out, _ = sjson.SetBytes(out, "id", fmt.Sprintf("chatcmpl-img-%d", time.Now().UnixNano()))
	out, _ = sjson.SetBytes(out, "created", unixNow())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", content)
	if usage := gjson.GetBytes(imageBody, "usage"); usage.Exists() && usage.IsObject() {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}
	return out, nil
}

func chatContentFromImagesResponse(imageBody []byte) (string, error) {
	var entries []map[string]string
	data := gjson.GetBytes(imageBody, "data")
	if !data.IsArray() {
		return "", fmt.Errorf("image response missing data array")
	}
	for _, item := range data.Array() {
		entry := map[string]string{}
		if value := strings.TrimSpace(item.Get("b64_json").String()); value != "" {
			entry["b64_json"] = value
		}
		if value := strings.TrimSpace(item.Get("url").String()); value != "" {
			entry["url"] = value
		}
		if value := strings.TrimSpace(item.Get("revised_prompt").String()); value != "" {
			entry["revised_prompt"] = value
		}
		if len(entry) > 0 {
			entries = append(entries, entry)
		}
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("image response did not contain images")
	}
	raw, errMarshal := json.Marshal(entries)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(raw), nil
}

func chatCompletionStreamChunks(model string, fullResponse []byte) [][]byte {
	id := gjson.GetBytes(fullResponse, "id").String()
	if id == "" {
		id = fmt.Sprintf("chatcmpl-img-%d", time.Now().UnixNano())
	}
	created := gjson.GetBytes(fullResponse, "created").Int()
	if created <= 0 {
		created = unixNow()
	}
	content := gjson.GetBytes(fullResponse, "choices.0.message.content").String()

	first := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
	first, _ = sjson.SetBytes(first, "id", id)
	first, _ = sjson.SetBytes(first, "created", created)
	first, _ = sjson.SetBytes(first, "model", model)

	body := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
	body, _ = sjson.SetBytes(body, "id", id)
	body, _ = sjson.SetBytes(body, "created", created)
	body, _ = sjson.SetBytes(body, "model", model)
	body, _ = sjson.SetBytes(body, "choices.0.delta.content", content)

	last := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	last, _ = sjson.SetBytes(last, "id", id)
	last, _ = sjson.SetBytes(last, "created", created)
	last, _ = sjson.SetBytes(last, "model", model)

	return [][]byte{first, body, last}
}

func collectImagesFromResponsesStream(streamID string, responseFormat string) ([]byte, error) {
	defer func() { _ = closeHostModelStream(streamID) }()
	acc := &sseFrameAccumulator{}
	for {
		raw, errRead := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
		if errRead != nil {
			return nil, errRead
		}
		var chunk pluginapi.HostModelStreamReadResponse
		if errDecode := json.Unmarshal(raw, &chunk); errDecode != nil {
			return nil, fmt.Errorf("decode host stream chunk: %w", errDecode)
		}
		if chunk.Error != "" {
			return nil, fmt.Errorf("%s", chunk.Error)
		}
		for _, frame := range acc.AddChunk(chunk.Payload) {
			if out, done, errFrame := processResponsesImageFrame(frame, responseFormat); errFrame != nil {
				return nil, errFrame
			} else if done {
				return out, nil
			}
		}
		if chunk.Done {
			for _, frame := range acc.Flush() {
				if out, done, errFrame := processResponsesImageFrame(frame, responseFormat); errFrame != nil {
					return nil, errFrame
				} else if done {
					return out, nil
				}
			}
			return nil, fmt.Errorf("stream disconnected before response.completed")
		}
	}
}

func processResponsesImageFrame(frame []byte, responseFormat string) ([]byte, bool, error) {
	for _, line := range bytes.Split(frame, []byte("\n")) {
		trimmed := bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(payload) {
			return nil, false, fmt.Errorf("invalid SSE data JSON: %s", string(payload))
		}
		if gjson.GetBytes(payload, "type").String() != "response.completed" {
			continue
		}
		results, createdAt, usageRaw, firstMeta, errExtract := extractImagesFromResponsesCompleted(payload)
		if errExtract != nil {
			return nil, false, errExtract
		}
		out, errBuild := buildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, responseFormat)
		return out, true, errBuild
	}
	return nil, false, nil
}

func extractImagesFromResponsesCompleted(payload []byte) (results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, err error) {
	if gjson.GetBytes(payload, "type").String() != "response.completed" {
		return nil, 0, nil, imageCallResult{}, fmt.Errorf("unexpected event type")
	}
	createdAt = gjson.GetBytes(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = unixNow()
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() != "image_generation_call" {
				continue
			}
			res := strings.TrimSpace(item.Get("result").String())
			if res == "" {
				continue
			}
			entry := imageCallResult{
				Result:        res,
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
				Size:          strings.TrimSpace(item.Get("size").String()),
				Background:    strings.TrimSpace(item.Get("background").String()),
				Quality:       strings.TrimSpace(item.Get("quality").String()),
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}
	if len(results) == 0 {
		return nil, 0, nil, imageCallResult{}, fmt.Errorf("upstream did not return image output")
	}
	if usage := gjson.GetBytes(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}
	return results, createdAt, usageRaw, firstMeta, nil
}

func buildImagesAPIResponse(results []imageCallResult, createdAt int64, usageRaw []byte, firstMeta imageCallResult, responseFormat string) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	responseFormat = normalizeResponseFormat(responseFormat)
	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			item, _ = sjson.SetBytes(item, "url", "data:"+mimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
		} else {
			item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if firstMeta.Background != "" {
		out, _ = sjson.SetBytes(out, "background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		out, _ = sjson.SetBytes(out, "output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		out, _ = sjson.SetBytes(out, "quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		out, _ = sjson.SetBytes(out, "size", firstMeta.Size)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}
	return out, nil
}

func buildImagesAPIResponseFromXAI(payload []byte, responseFormat string) ([]byte, error) {
	results, createdAt, usageRaw, errExtract := extractXAIImagesResponse(payload)
	if errExtract != nil {
		return nil, errExtract
	}
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	responseFormat = normalizeResponseFormat(responseFormat)
	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			if img.URL != "" {
				item, _ = sjson.SetBytes(item, "url", img.URL)
			} else {
				item, _ = sjson.SetBytes(item, "url", "data:"+mimeTypeFromOutputFormat(img.MimeType)+";base64,"+img.B64JSON)
			}
		} else if img.B64JSON != "" {
			item, _ = sjson.SetBytes(item, "b64_json", img.B64JSON)
		} else {
			item, _ = sjson.SetBytes(item, "url", img.URL)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}
	return out, nil
}

func extractXAIImagesResponse(payload []byte) (results []xaiImageResult, createdAt int64, usageRaw []byte, err error) {
	if !json.Valid(payload) {
		return nil, 0, nil, fmt.Errorf("upstream returned invalid image response JSON")
	}
	createdAt = gjson.GetBytes(payload, "created").Int()
	if createdAt <= 0 {
		createdAt = unixNow()
	}
	data := gjson.GetBytes(payload, "data")
	if data.IsArray() {
		for _, item := range data.Array() {
			result := xaiImageResult{
				B64JSON:       strings.TrimSpace(item.Get("b64_json").String()),
				URL:           strings.TrimSpace(item.Get("url").String()),
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				MimeType:      strings.TrimSpace(item.Get("mime_type").String()),
			}
			if result.MimeType == "" {
				result.MimeType = mimeTypeFromOutputFormat(strings.TrimSpace(item.Get("output_format").String()))
			}
			if result.MimeType == "" {
				result.MimeType = "image/png"
			}
			if result.B64JSON == "" && result.URL == "" {
				continue
			}
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return nil, 0, nil, fmt.Errorf("upstream did not return image output")
	}
	if usage := gjson.GetBytes(payload, "usage"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}
	return results, createdAt, usageRaw, nil
}

func mimeTypeFromOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

type sseFrameAccumulator struct {
	pending []byte
}

func (a *sseFrameAccumulator) AddChunk(chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return nil
	}
	a.pending = append(a.pending, chunk...)
	var frames [][]byte
	for {
		idx := bytes.Index(a.pending, []byte("\n\n"))
		if idx < 0 {
			break
		}
		frame := append([]byte(nil), a.pending[:idx+2]...)
		frames = append(frames, frame)
		a.pending = append([]byte(nil), a.pending[idx+2:]...)
	}
	return frames
}

func (a *sseFrameAccumulator) Flush() [][]byte {
	if len(bytes.TrimSpace(a.pending)) == 0 {
		a.pending = nil
		return nil
	}
	frame := append([]byte(nil), a.pending...)
	a.pending = nil
	return [][]byte{frame}
}
