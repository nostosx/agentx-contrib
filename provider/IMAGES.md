# Image generation and editing

## Gemini implementation

`gemini.NewImageGenerator` implements the same four core imagegen interfaces,
using the established generateContent/streamGenerateContent protocol and the
existing Gemini/Vertex authentication and input converter. It defaults to
`gemini-nano-banana-2.1`; use gemini.WithModel for another supported image model.

Gemini image configuration differs: Size is `1K`, `2K`, `4K` (or `512` on
models that accept it), with a separate AspectRatio such as `16:9`. N is zero
or one. Edits accept reference images and a prompt, up to the selected model's
limit (the adapter caps input count at 14). Mask, markup, quality, compression,
background and safety-identifier typed options are rejected before requests;
they are not silently approximated. OutputFormat may be empty or `png`.
The returned bytes/MIME type are authoritative. Extra Gemini request fields
use ProviderOptions["gemini"], with protected typed image fields.

Streaming separates thought-image previews from final images; PartialImages
bounds emitted previews, rather than guaranteeing the model produces them.
Completion requires a final STOP reason and actual final image bytes. Text-only,
blocked and truncated results fail. Thought signatures remain on image Parts
for subsequent Gemini conversation replay. Returned usage includes available
modality details. Preview and final response sizes are bounded as for OpenAI.

`providercheck -provider=gemini-images -model=<explicit-model>` shares the same
live gate and image evidence reporting. Default generate/stream checks use
one PNG, 1K size and 1:1 aspect ratio without an unsupported quality parameter.
Edit additionally requires the explicit media fixture. Nothing runs a paid
request by default. See Google's [generateContent image guide](https://ai.google.dev/gemini-api/docs/generate-content/image-generation).

## Artifact persistence

Set imagegen.ToolOptions.Artifacts to an artifact.Store and ArtifactScope to
trusted application identity. Implementations include filestore.Artifacts and
the S3 adapter backed by servex-contrib. Saved keys are in tool Metadata under
"artifacts". A storage failure returns imagegen.ArtifactError with the generated
response and saved keys, so the application can persist without regenerating.
See [artifact integration](../artifact/README.md).

## OpenAI implementation

Image contracts require agentx v1.3.0 and image adapters require provider v1.3.0.
Install their published versions without a source workspace:

```sh
mkdir image-demo
cd image-demo
go mod init example.com/image-demo
go get github.com/nostosx/agentx@v1.3.0 github.com/nostosx/agentx-contrib/provider@v1.3.0
```

`openai.ImageGenerator` calls the Images API directly over `net/http` and
implements `imagegen.Generator`, `Editor`, `Streamer` and `EditStreamer`.
It is safe for concurrent use. It does not change the conversation adapters
or require a conversation model for direct generation.

## Generate and save an image

Save this as `main.go` in the workspace above. Set `OPENAI_API_KEY` and run
`go run .`. This sends one paid generation request. `OPENAI_BASE_URL` or
`WithBaseURL` selects a compatible endpoint; use a trusted endpoint.

```go
package main

import (
    "context"
    "log"
    "os"
    "time"

    "github.com/nostosx/agentx-contrib/provider/openai"
    "github.com/nostosx/agentx/imagegen"
)

func main() {
    images, err := openai.NewImageGenerator(
        openai.WithModel("gpt-image-2.5-flare"),
    )
    if err != nil {
        log.Fatal(err)
    }
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()
    result, err := images.Generate(ctx, &imagegen.Request{
        Prompt: "A watercolor sailboat at sunset",
        N: 1, Size: "1536x864", Quality: "high", OutputFormat: "png",
    })
    if err != nil {
        log.Fatal(err)
    }
    if err := os.WriteFile("sailboat.png", result.Images[0].Part.Data, 0o600); err != nil {
        log.Fatal(err)
    }
    log.Printf("request=%s model=%s usage=%+v", result.RequestID, result.Model, result.Usage)
}
```

The default model is `gpt-image-2.5-sunburst`. `WithModel` and `Request.Model`
also accept Flare, dated snapshots and compatible model IDs. There is no hard
model allowlist. `Request.Model` overrides the constructor's model per call.
Sunburst emphasizes editing precision; Flare emphasizes everyday speed.

## Edit images

The following fragments reuse `ctx` and `images` above; import the core
`model` package as well. The Images API accepts JSON references, so inline
bytes become data URLs without a separate Files upload:

```go
edited, err := images.Edit(ctx, &imagegen.EditRequest{
    Request: imagegen.Request{
        Prompt: "Replace the sky with a starry night",
        OutputFormat: "png", Quality: "max",
    },
    Images: []model.Part{model.ImagePart("image/png", sourcePNG)},
    Mask: new(model.ImagePart("image/png", maskPNG)),
})
```

Handle `err` before reading `edited.Images`. Inputs may also use
`model.ImageURLPart("https://...")` or
`model.FilePart(model.PartImage, "openai", "file-...", "image/png")`.
Exactly one of bytes, URL and file ID is required per part. File IDs from a
different provider are rejected. The client does not fetch URLs or load paths.
Up to 16 input images are accepted. Inline PNG/JPEG/WebP data URLs are limited
to 20 MiB encoded; masks and markup require PNG when supplied inline.

`Mask` identifies editable regions, with dimensions/transparency checked by
the API. `Markup` is an optional transparent visual annotation supported by
GPT Image 2.5; it cannot be combined with `Mask`. Set `Markup` in the same way
as `Mask`, using bytes, a URL or file ID. Model-specific support, image
dimensions, pixel limits and remote image formats remain API validation.

## Output and provider options

- `N`: zero uses the API default of one; explicit values range from 1 to 10.
- `Size`: `auto`, standard dimensions or model-supported `WIDTHxHEIGHT`.
- `Quality`: `auto`, `low`, `medium`, `high`, `xhigh` or `max`.
- `OutputFormat`: `png` (API default), `jpeg` or `webp`.
- `Background`: `auto`, `opaque` or `transparent`; transparent JPEG is rejected.
- `OutputCompression`: pointer to 0–100 for explicit JPEG/WebP output, including
  zero; nil omits it. PNG does not accept compression quality.
- `SafetyIdentifier`: optional application-provided identifier up to 128 characters.
- `ProviderOptions["openai"]`: extra API fields such as `moderation`, or
  `input_fidelity` on models that accept it. Typed fields cannot be overridden;
  retired `response_format`, `style` and `user` options are rejected.

GPT Image returns base64 bytes, which are decoded into `Image.Part.Data` with
the output MIME type. Compatible URL results are returned in `Part.URL` without
downloading them. `RevisedPrompt` is retained if supplied. Responses preserve
the request ID, creation time, reported settings and text/image token details.
`Usage == nil` means the API did not report usage, not zero billed usage.
`Model` is the requested model if the API does not report a resolved model ID.

Responses are limited to 256 MiB, individual SSE lines to 96 MiB. API errors
reuse `model.APIError`, including Retry-After and existing retry classification;
invalid requests are permanent. Calls use the supplied context and HTTP client.
Set a timeout explicitly. The client does not automatically retry: a connection
failure can leave an already-billed generation's outcome unknown.

## Streaming

The adapter streams one output image per request. Use `N: 1` or its zero
default. Request zero to three previews with `PartialImages`:

```go
for event, err := range images.Stream(ctx, &imagegen.Request{
    Prompt: "A watercolor sailboat at sunset", PartialImages: 2,
}) {
    if err != nil {
        log.Fatal(err)
    }
    if event.Image != nil {
        // Display this preview; it is not a successful final result.
    }
    if event.Response != nil {
        // Persist/display event.Response.Images and record its usage.
    }
}
```

`StreamEdit` accepts an `EditRequest` and has the same event contract.
An error or EOF before a completion is a failure, even after previews.
Breaking iteration closes/cancels the HTTP request. No partial-image delta
is mistaken for completion. Parallel independent calls may be used for
multiple streamed output images; the application owns their cost/concurrency.

## Agent tool

Wrap the same client and register it with a normal conversation agent:

```go
imageTool := imagegen.Tool(images, imagegen.ToolOptions{
    Request: imagegen.Request{
        Model: "gpt-image-2.5-flare", Quality: "high", OutputFormat: "png",
    },
    MaxImages: 2,
    ResolveImage: func(ctx context.Context, ref string) (model.Part, error) {
        return authorizedAssets.ResolveImage(ctx, ref)
    },
})
a, err := agent.New("artist",
    agent.WithModel(conversationModel),
    agent.WithTools(imageTool),
)
```

`authorizedAssets` is an application dependency, not a framework global:
authorize the reference before loading bytes or returning a URL/file ID.
Without `ResolveImage`, the tool's schema exposes generation only. With a
resolver and an `Editor`, its `images`, `mask` and `markup` arguments accept
application references. The default tool name is `generate_image`.

The agent can choose prompt, count (bounded by `MaxImages`, default one), size,
quality, format and background. It cannot choose model, safety identifier or
provider options. The tool is neither read-only nor concurrent by default.
Additional `tool.Option` arguments can select existing runtime policies, such
as `tool.RequireApproval()` when the application needs it.

Images live in tool result `Parts`; usage/settings/request ID live in
`Metadata`. Run events and conversation tool results expose them to the UI;
`a.Ask` returns text only. Session/checkpoint serialization retains the image
parts. The application owns long-term asset storage and any uploads or
publication. Image costs are separate from conversation-model token limits.

## Explicit live acceptance

Ordinary tests use local HTTP fixtures and deterministic Agent models. They
do not contact OpenAI or demonstrate account/model access. The existing
`providercheck` command supports `-provider=openai-images` and requires an
explicit model. Without `-live`, it reads no credentials or fixture files and
reports `unverified` with a nonzero exit:

```sh
go run ./cmd/providercheck -provider=openai-images -model=gpt-image-2.5-flare
```

With matching unpublished sources, use the temporary workspace/modfile
described above. To deliberately send three paid requests (generate, stream,
edit), run from this provider module with an authorized image fixture:

```sh
go run ./cmd/providercheck -live -provider=openai-images \
  -model=gpt-image-2.5-flare -checks=generate,stream,edit \
  -media=/absolute/path/to/fixture.png -media-type=image/png \
  -request-timeout=5m -timeout=16m
```

Image defaults are `generate,stream`; edit is explicit and requires a fixture
of at most 5 MiB. Each check requests one PNG at 1024×1024, low quality, with
one preview for streaming. There are no automatic retries. Reports include
decoded dimensions, byte count, SHA-256, request ID and any returned usage;
they omit image contents, prompts, keys and raw API errors. A valid PNG and
completion are required, not merely HTTP 200. Reports do not certify other
models, image quality, requested-versus-billed resolution or actual charges.

API contracts: [generation](https://developers.openai.com/api/reference/resources/images/methods/generate),
[editing](https://developers.openai.com/api/reference/resources/images/methods/edit),
[image guide](https://developers.openai.com/api/docs/guides/image-generation).
