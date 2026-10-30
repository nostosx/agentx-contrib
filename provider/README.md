# Provider adapters and live acceptance

This module contains OpenAI Responses and Chat Completions, Anthropic Messages,
Gemini/Vertex AI and Amazon Bedrock Converse adapters. The normal tests use local
HTTP fixtures; they do not contact a provider or prove current live API behavior.

Independent image generation/editing is available through
`openai.NewImageGenerator` and `gemini.NewImageGenerator`; see [IMAGES.md](IMAGES.md) for the published
contracts, GPT Image 2.5 examples, Agent tools and explicit live acceptance.

## Explicit live acceptance

`providercheck` is a verification command, not part of ordinary tests or CI.
From this module directory, inspect the requested checks without accessing
credentials or sending requests:

```sh
GOWORK=off go run ./cmd/providercheck -provider=openai -model=YOUR_MODEL
```

The JSON report says `unverified` and the command exits nonzero. Add `-live`
only when intentionally allowing network requests and the provider's charges.
It uses the selected adapter's documented environment variables; it does not
load credential files, discover models, or silently choose a generation model.

```sh
GOWORK=off go run ./cmd/providercheck -live -provider=openai -model=YOUR_MODEL \
  -checks=generate,stream,tools,structured,embedding \
  -embedding-model=text-embedding-3-small
```

Select `openai-images` or `gemini-images` for independent image generation;
these default to generate/stream and accept an explicit edit check with a media
fixture. See IMAGES.md for provider-specific request settings and image evidence.
Select `openai-chat` to exercise Chat Completions separately; other provider
names are `anthropic`, `gemini` and `bedrock`. Optional `-base-url` overrides
the selected adapter's API endpoint, including compatible services. Credentials
are sent to that configured endpoint; set it deliberately. For Bedrock, configure
the region and either its bearer token or AWS signing credentials as usual.

Checks are explicit and every requested check must pass. Capabilities omitted
from `-checks` remain `not_requested`, not verified. Defaults are `generate`,
`stream`, `tools` and `structured`; `embedding` additionally requires an explicit
`-embedding-model` and a supported adapter (OpenAI, Gemini or Bedrock Titan).

- `generate` validates a small response, normal completion and token usage.
- `stream` requires a final completion event, valid final response and usage.
- `tools` validates one tool invocation and a second model call with its result.
- `structured` validates the returned JSON object against the requested schema.
- `embedding` checks two finite, nonzero vectors with consistent dimensions.
- `media` sends an inline fixture supplied with `-media` and `-media-type`.
  Images, documents, audio and video are inferred from the MIME type. The file
  must contain at most 5 MiB, and the selected provider/model must support it.
  This checks acceptance of the content and a textual response; it does not
  evaluate the quality of media understanding or generated media output.

Each generation request is limited to 512 output tokens by default; `-max-tokens`
accepts 64–4096. A check has a 30s default deadline (`-request-timeout`, maximum
5m); the full run has a 3m deadline (`-timeout`, maximum 30m). There are no
automatic retries. Default checks make five generation requests: one each for
generation, streaming and structured output, and two for the tool roundtrip.
Embedding and media add their own requests. These bounds limit work, not an
exact monetary cost, which depends on the selected model and media.

Reports include UTC time, provider, explicit model names, per-check status,
duration, token usage and fixed error categories. They omit credentials,
endpoint URLs, request/response content and raw provider errors. A missing key,
disabled live gate, invalid configuration, failed or timed-out check produces
a nonzero exit. Keep reports scoped to their tested model, date and checks;
one passing report does not certify other models or protocols.

## Additional embedding backends

`gemini.NewEmbedder(gemini.WithVertex(project, location),
gemini.WithTokenSource(tokenSource))` uses the Vertex AI publisher-model
`:predict` API and defaults to `gemini-embedding-001`. It uses the same token
source as the generation adapter, sends task types and output dimensionality,
and disables silent input truncation. The default batch size is one input for
compatibility across publisher models; `WithBatchSize` can select up to 250
when the chosen model permits it. The regular Gemini API retains its existing
`batchEmbedContents` protocol and default model.

`bedrock.NewEmbedder(opts...)` uses InvokeModel with
`amazon.titan-embed-text-v2:0` by default. `WithModel` also accepts
`amazon.titan-embed-text-v1`. Authentication, HTTP client and endpoint options
are shared with Converse. `WithDimensions` accepts 256, 512 or 1024 for V2;
G1 has no dimension option. It requests normalized float vectors and sends
one request per input in input order. Other Bedrock embedding families use
different wire formats and are not accepted by this implementation.

The embedding implementations are covered by local protocol fixtures. These
tests do not constitute live Google Cloud or AWS verification.

## Cohere reranking

`cohere.NewReranker(opts...)` implements `rag.Reranker` with Cohere's
[`POST /v2/rerank`](https://docs.cohere.com/v2/reference/rerank). It reads
`COHERE_API_KEY`, defaults to `rerank-v5.0-pro`, and accepts `WithAPIKey`,
`WithTokenSource`, `WithBaseURL`, `WithModel` and `WithHTTPClient` overrides.
`WithTopN` and `WithMinScore` can reduce results. `WithMaxTokensPerDoc` controls
the API's document truncation budget; zero leaves its documented default.

Use it as the `Reranker` of the core `rag.Reranked` pipeline. Chunk context is
included with its content, API relevance scores replace first-stage scores,
and input hits and chunks remain unchanged. A call accepts at most 1000
candidates; it rejects malformed indices, duplicate results, missing scores
and invalid score ordering instead of associating a score with the wrong chunk.
API failures retain `model.APIError`, so the existing retrieval pipeline can
apply its configured failure fallback. No automatic retry is added.

This adapter has local HTTP protocol tests. The live-provider command above
currently verifies generation providers and embeddings; it does not run Cohere
reranking or certify its live availability.
