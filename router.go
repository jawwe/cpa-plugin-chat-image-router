package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type chatImageRouterPlugin struct {
	cfg pluginConfig
}

var _ pluginapi.ModelRouter = (*chatImageRouterPlugin)(nil)
var _ pluginapi.ProviderExecutor = (*chatImageRouterPlugin)(nil)

func (p *chatImageRouterPlugin) Identifier() string {
	return pluginID
}

func (p *chatImageRouterPlugin) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	if p == nil || !p.cfg.Enabled {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(req.SourceFormat), "openai") {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	if !p.cfg.matchesModel(req.RequestedModel) {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	if _, errBuild := buildImageRequestFromChat(req.Body, p.cfg); errBuild != nil {
		return pluginapi.ModelRouteResponse{Handled: false}, nil
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "chat_completion_image_model",
	}, nil
}

func (p *chatImageRouterPlugin) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.execute(ctx, req, "")
}

func (p *chatImageRouterPlugin) execute(_ context.Context, req pluginapi.ExecutorRequest, hostCallbackID string) (pluginapi.ExecutorResponse, error) {
	plan, errBuild := buildImageRequestFromChat(executorRequestBody(req), p.cfg)
	if errBuild != nil {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadRequest, message: errBuild.Error()}
	}

	imageBody, headers, errRun := p.runImagePlan(plan, hostCallbackID)
	if errRun != nil {
		return pluginapi.ExecutorResponse{}, errRun
	}
	chatBody, errChat := buildChatCompletionResponse(req.Model, imageBody, plan.ResponseFormat)
	if errChat != nil {
		return pluginapi.ExecutorResponse{}, statusError{status: http.StatusBadGateway, message: errChat.Error()}
	}
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/json")
	return pluginapi.ExecutorResponse{Payload: chatBody, Headers: headers}, nil
}

func (p *chatImageRouterPlugin) ExecuteStream(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return pluginapi.ExecutorStreamResponse{}, statusError{status: http.StatusNotImplemented, message: "direct in-process streaming is not implemented; use the RPC plugin ABI"}
}

func (p *chatImageRouterPlugin) executeStream(_ context.Context, req pluginapi.ExecutorRequest, hostCallbackID string, streamID string) (map[string]any, error) {
	if strings.TrimSpace(streamID) == "" {
		return nil, statusError{status: http.StatusBadRequest, message: "stream_id is required"}
	}
	go func() {
		errMsg := ""
		defer func() {
			if recovered := recover(); recovered != nil {
				errMsg = fmt.Sprintf("chat image stream panic: %v", recovered)
			}
			closePluginStream(streamID, errMsg)
		}()

		resp, errExecute := p.execute(context.Background(), req, hostCallbackID)
		if errExecute != nil {
			errMsg = errExecute.Error()
			return
		}
		for _, chunk := range chatCompletionStreamChunks(req.Model, resp.Payload) {
			if errEmit := emitPluginStreamChunk(streamID, chunk); errEmit != nil {
				errMsg = errEmit.Error()
				return
			}
		}
	}()
	return map[string]any{
		"headers": http.Header{"Content-Type": []string{"text/event-stream"}},
	}, nil
}

func (p *chatImageRouterPlugin) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)}, nil
}

func (p *chatImageRouterPlugin) HttpRequest(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, statusError{status: http.StatusNotImplemented, message: "http_request is not implemented"}
}

func (p *chatImageRouterPlugin) runImagePlan(plan imageRequestPlan, hostCallbackID string) ([]byte, http.Header, error) {
	if isGPTImage2Model(plan.Model) {
		body, headers, errRun := executeGPTImage2Plan(plan, p.cfg, hostCallbackID)
		return body, headers, errRun
	}
	raw, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai-image",
			ExitProtocol:  "openai-image",
			Model:         plan.Model,
			Stream:        false,
			Body:          plan.ImageRequest,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, nil, errCall
	}
	var resp pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, nil, fmt.Errorf("decode host model response: %w", errDecode)
	}
	if resp.StatusCode >= 400 {
		return nil, resp.Headers, statusError{status: resp.StatusCode, message: string(resp.Body)}
	}
	imageBody, errNormalize := buildImagesAPIResponseFromXAI(resp.Body, plan.ResponseFormat)
	if errNormalize != nil {
		return nil, resp.Headers, errNormalize
	}
	return imageBody, resp.Headers, nil
}

func executeGPTImage2Plan(plan imageRequestPlan, cfg pluginConfig, hostCallbackID string) ([]byte, http.Header, error) {
	mainModel := responseMainModel(plan.Model, cfg.MainModel)
	responsesReq := buildImagesResponsesRequest(plan.Prompt, plan.Images, plan.ToolRequest, mainModel)
	raw, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: "openai-response",
			ExitProtocol:  "openai-response",
			Model:         mainModel,
			Stream:        true,
			Body:          responsesReq,
		},
		HostCallbackID: hostCallbackID,
	})
	if errCall != nil {
		return nil, nil, errCall
	}
	var resp pluginapi.HostModelStreamResponse
	if errDecode := json.Unmarshal(raw, &resp); errDecode != nil {
		return nil, nil, fmt.Errorf("decode host model stream response: %w", errDecode)
	}
	if resp.StatusCode >= 400 {
		_ = closeHostModelStream(resp.StreamID)
		return nil, resp.Headers, statusError{status: resp.StatusCode, message: fmt.Sprintf("host model stream status %d", resp.StatusCode)}
	}
	body, errCollect := collectImagesFromResponsesStream(resp.StreamID, plan.ResponseFormat)
	if errCollect != nil {
		return nil, resp.Headers, errCollect
	}
	return body, resp.Headers, nil
}

func executorRequestBody(req pluginapi.ExecutorRequest) []byte {
	if len(req.OriginalRequest) > 0 {
		return append([]byte(nil), req.OriginalRequest...)
	}
	return append([]byte(nil), req.Payload...)
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	_, errCall := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	return errCall
}

func closePluginStream(streamID, errMsg string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    strings.TrimSpace(errMsg),
	})
}

func closeHostModelStream(streamID string) error {
	if strings.TrimSpace(streamID) == "" {
		return nil
	}
	_, errCall := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

type statusError struct {
	status  int
	message string
}

func (e statusError) Error() string {
	return e.message
}

func (e statusError) StatusCode() int {
	if e.status <= 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

func unixNow() int64 {
	return time.Now().Unix()
}
