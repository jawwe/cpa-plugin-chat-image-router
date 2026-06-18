# CPA Chat Image Router Plugin

CLIProxyAPI dynamic-library plugin that routes selected `/v1/chat/completions` image-model requests, such as `gpt-image-2`, into the image generation/edit path and returns the generated image payload as an OpenAI chat completion response.

## Behavior

- Declares `model_router` and `executor`.
- Intercepts OpenAI-format chat completion requests for configured image models.
- Extracts text content as the image prompt.
- Treats `image_url` parts or top-level `image` / `images` values as edit inputs.
- Calls CLIProxyAPI host model callbacks instead of copying credentials or bypassing host routing.
- Wraps the image API result into an assistant message whose `content` is a JSON array of image result objects.

## Build

Requires Go with CGO enabled and a working C compiler.

```bash
go build -buildmode=c-shared -o cpa-plugin-chat-image-router.dll .
```

Use `.so` on Linux/FreeBSD and `.dylib` on macOS.

## Install

Place the dynamic library in one of CLIProxyAPI's plugin discovery directories:

```text
plugins/<GOOS>/<GOARCH>
plugins
```

The plugin ID is the dynamic library basename without the platform extension. With the build command above, configure `cpa-plugin-chat-image-router`.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-plugin-chat-image-router:
      enabled: true
      priority: 1
      models:
        - gpt-image-2
      main_model: gpt-5.4-mini
      response_format: b64_json
```

`response_format` accepts `b64_json` or `url`.

After starting CLIProxyAPI, check `GET /v0/management/plugins` and confirm this plugin has `registered: true` and `effective_enabled: true`.

## Development

```bash
go test ./...
```

The plugin uses `host.model.execute`, `host.model.execute_stream`, `host.model.stream_read`, `host.model.stream_close`, `host.stream.emit`, and `host.stream.close` callbacks from the CLIProxyAPI plugin ABI.
