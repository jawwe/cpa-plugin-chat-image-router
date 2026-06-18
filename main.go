package main

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

var pluginVersion = "0.1.0"

func buildPlugin(configYAML []byte) (pluginapi.Plugin, error) {
	cfg, errParse := parseConfig(configYAML)
	if errParse != nil {
		return pluginapi.Plugin{}, errParse
	}
	p := &chatImageRouterPlugin{cfg: cfg}
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "When false, the router declines all chat image-model requests."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Chat completion model names to route to the image pipeline. Defaults to gpt-image-2."},
				{Name: "main_model", Type: pluginapi.ConfigFieldTypeString, Description: "Responses model used to drive the gpt-image-2 image_generation tool."},
				{Name: "response_format", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"b64_json", "url"}, Description: "Default image response format when the chat request does not provide one."},
			},
		},
		Capabilities: pluginapi.Capabilities{
			ModelRouter:           p,
			Executor:              p,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
		},
	}, nil
}
