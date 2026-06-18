package main

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildImageRequestFromChatGeneration(t *testing.T) {
	cfg := defaultPluginConfig()
	plan, err := buildImageRequestFromChat([]byte(`{
		"model":"gpt-image-2",
		"messages":[{"role":"user","content":"draw a red cube"}],
		"size":"1024x1024"
	}`), cfg)
	if err != nil {
		t.Fatalf("buildImageRequestFromChat() error = %v", err)
	}
	if plan.Endpoint != "/v1/images/generations" {
		t.Fatalf("Endpoint = %q", plan.Endpoint)
	}
	if got := gjson.GetBytes(plan.ImageRequest, "prompt").String(); got != "draw a red cube" {
		t.Fatalf("prompt = %q", got)
	}
	if got := gjson.GetBytes(plan.ToolRequest, "action").String(); got != "generate" {
		t.Fatalf("tool action = %q", got)
	}
	if got := gjson.GetBytes(plan.ToolRequest, "size").String(); got != "1024x1024" {
		t.Fatalf("tool size = %q", got)
	}
}

func TestBuildImageRequestFromChatEdit(t *testing.T) {
	cfg := defaultPluginConfig()
	plan, err := buildImageRequestFromChat([]byte(`{
		"model":"gpt-image-2",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"make it blue"},
				{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
			]
		}]
	}`), cfg)
	if err != nil {
		t.Fatalf("buildImageRequestFromChat() error = %v", err)
	}
	if plan.Endpoint != "/v1/images/edits" {
		t.Fatalf("Endpoint = %q", plan.Endpoint)
	}
	if len(plan.Images) != 1 || plan.Images[0] != "https://example.com/a.png" {
		t.Fatalf("Images = %#v", plan.Images)
	}
	if got := gjson.GetBytes(plan.ToolRequest, "action").String(); got != "edit" {
		t.Fatalf("tool action = %q", got)
	}
	if got := gjson.GetBytes(plan.ImageRequest, "images.0.image_url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image request images = %s", prettyJSON(plan.ImageRequest))
	}
}

func TestConfigModelMatchingUsesBaseName(t *testing.T) {
	cfg := defaultPluginConfig()
	if !cfg.matchesModel("openai/gpt-image-2") {
		t.Fatal("matchesModel(openai/gpt-image-2) = false")
	}
	if cfg.matchesModel("gpt-5.4-mini") {
		t.Fatal("matchesModel(gpt-5.4-mini) = true")
	}
}
