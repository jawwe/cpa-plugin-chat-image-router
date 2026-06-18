package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	pluginID                 = "cpa-plugin-chat-image-router"
	defaultImagesMainModel   = "gpt-5.4-mini"
	defaultImagesToolModel   = "gpt-image-2"
	defaultImageResponseType = "b64_json"
)

type pluginConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Models         []string `yaml:"models"`
	MainModel      string   `yaml:"main_model"`
	ResponseFormat string   `yaml:"response_format"`
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:        true,
		Models:         []string{defaultImagesToolModel},
		MainModel:      defaultImagesMainModel,
		ResponseFormat: defaultImageResponseType,
	}
}

func parseConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return cfg, fmt.Errorf("invalid chat-image-router config: %w", errUnmarshal)
		}
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{defaultImagesToolModel}
	}
	cfg.MainModel = strings.TrimSpace(cfg.MainModel)
	if cfg.MainModel == "" {
		cfg.MainModel = defaultImagesMainModel
	}
	cfg.ResponseFormat = normalizeResponseFormat(cfg.ResponseFormat)
	return cfg, nil
}

func (cfg pluginConfig) matchesModel(model string) bool {
	model = imageModelBase(model)
	if model == "" {
		return false
	}
	for _, candidate := range cfg.Models {
		if strings.EqualFold(imageModelBase(candidate), model) {
			return true
		}
	}
	return false
}

func normalizeResponseFormat(format string) string {
	if strings.EqualFold(strings.TrimSpace(format), "url") {
		return "url"
	}
	return defaultImageResponseType
}

func imageModelBase(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}
