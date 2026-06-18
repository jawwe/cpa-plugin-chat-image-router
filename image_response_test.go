package main

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildChatCompletionResponseWrapsImages(t *testing.T) {
	raw, err := buildChatCompletionResponse("gpt-image-2", []byte(`{
		"created": 123,
		"data": [{"b64_json":"AA==","revised_prompt":"red cube"}],
		"usage": {"input_tokens": 1}
	}`), "b64_json")
	if err != nil {
		t.Fatalf("buildChatCompletionResponse() error = %v", err)
	}
	if got := gjson.GetBytes(raw, "object").String(); got != "chat.completion" {
		t.Fatalf("object = %q", got)
	}
	content := gjson.GetBytes(raw, "choices.0.message.content").String()
	if got := gjson.Get(content, "0.b64_json").String(); got != "AA==" {
		t.Fatalf("content image = %q; content=%s", got, content)
	}
	if got := gjson.GetBytes(raw, "usage.input_tokens").Int(); got != 1 {
		t.Fatalf("usage.input_tokens = %d", got)
	}
}

func TestProcessResponsesImageFrameBuildsImageResponse(t *testing.T) {
	frame := []byte("event: response.completed\n" +
		`data: {"type":"response.completed","response":{"created_at":123,"output":[{"type":"image_generation_call","result":"AA==","output_format":"png","revised_prompt":"cube"}]}}` +
		"\n\n")
	out, done, err := processResponsesImageFrame(frame, "url")
	if err != nil {
		t.Fatalf("processResponsesImageFrame() error = %v", err)
	}
	if !done {
		t.Fatal("done = false")
	}
	if got := gjson.GetBytes(out, "data.0.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("url = %q; body=%s", got, out)
	}
}

func TestChatCompletionStreamChunks(t *testing.T) {
	full, err := buildChatCompletionResponse("gpt-image-2", []byte(`{"data":[{"url":"https://example.com/a.png"}]}`), "url")
	if err != nil {
		t.Fatalf("buildChatCompletionResponse() error = %v", err)
	}
	chunks := chatCompletionStreamChunks("gpt-image-2", full)
	if len(chunks) != 3 {
		t.Fatalf("chunks len = %d", len(chunks))
	}
	if got := gjson.GetBytes(chunks[0], "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("first role = %q", got)
	}
	if got := gjson.GetBytes(chunks[2], "choices.0.finish_reason").String(); got != "stop" {
		t.Fatalf("finish_reason = %q", got)
	}
}
