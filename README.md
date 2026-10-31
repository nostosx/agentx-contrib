# agentx-contrib

English | [简体中文](README.zh-CN.md)

agentx-contrib holds the implementations of
[agentx](https://github.com/nostosx/agentx) that need third-party
dependencies or are specific to one service: model providers, file and SQL
stores, OpenTelemetry, logging and retries. The agentx module itself uses
the Go standard library and kitx, and keeps the contracts with one in-memory
reference implementation each; see the
[agentx README](https://github.com/nostosx/agentx#readme) for the
framework.

Each directory is its own module, so a program depends only on what it
imports.

> Requires Go 1.27.3 or later. Changes are
> recorded in [CHANGELOG.md](CHANGELOG.md).

## Modules

| Module | Packages | Implements | Third-party dependencies |
| --- | --- | --- | --- |
| `github.com/nostosx/agentx-contrib/web` | `web` | readable HTML/Markdown, attributed links, optional cache; core Reader/Extractor | Readability, HTML-to-Markdown, Servex HTTP/Cache Loader |
| `github.com/nostosx/agentx-contrib/browser` | `browser` | dynamic pages, interaction, screenshots/downloads; isolated Chromium Session | chromedp; application Chromium and core Extractor |
| `github.com/nostosx/agentx-contrib/loader` | `loader` | scoped GitHub/Notion/Feishu snapshots and incremental cursors | Servex HTTP and Cache/Codec contracts |
| `github.com/nostosx/agentx-contrib/search` | `search` | Tavily/Serper core `search.Searcher`, with an Agent tool adapter | none |
| `github.com/nostosx/agentx-contrib/retrieval` | `retrieval` | Qdrant/Elasticsearch `rag.Retriever` and non-atomic `rag.SourceWriter` | Servex HTTP |
| `github.com/nostosx/agentx-contrib/debug` | `debug` | bounded `telemetry.Tracer`, authenticated HTTP viewer and evaluation comparison | none |
| `github.com/nostosx/agentx-contrib/artifact` | `artifact` | core `artifact.Store` backed by servex-contrib S3 object storage | servex-contrib/storage/s3 and its SDK types |
| `github.com/nostosx/agentx-contrib/embedding` | `embedding` | cached `memory.Embedder` using servex Cache and contrib Loader | servex, cache/loader; tests compose memory cache and JSON codec |
| `github.com/nostosx/agentx-contrib/provider` | `provider/openai`, `provider/anthropic`, `provider/gemini`, `provider/bedrock`, `provider/cohere` | `model.Model`; `imagegen.Generator/Editor/Streamer/EditStreamer` (OpenAI/Gemini); `memory.Embedder` (OpenAI, Gemini/Vertex, Bedrock Titan); `rag.Reranker` (Cohere) | `github.com/nostosx/kitx/dataurl`, `github.com/nostosx/kitx/utf8text` |
| `github.com/nostosx/agentx-contrib/otel` | `otel` | `telemetry.Tracer` and `telemetry.Meter` over OpenTelemetry | `go.opentelemetry.io/otel` with its `trace`, `metric` and `log` APIs (the SDK only in tests) |
| `github.com/nostosx/agentx-contrib/sqlstore` | `sqlstore` | `session.Store`, `loop.Checkpointer`, `workflow.Checkpointer`, `memory.Store` and encrypted `mcp.TokenStore` on SQLite, PostgreSQL and MySQL | none in the package; its tests use `modernc.org/sqlite`, `github.com/jackc/pgx/v5` and `github.com/go-sql-driver/mysql` |
| `github.com/nostosx/agentx-contrib/filestore` | `filestore` | `session.Store`, `loop.Checkpointer`, `workflow.Checkpointer`, `memory.Store`, `rag.VectorStore`, `artifact.Store` and `mcp.TokenStore` in JSON files | `github.com/nostosx/kitx/atomicfile` |
| `github.com/nostosx/agentx-contrib/middleware` | `middleware/logging`, `middleware/retry`, `middleware/fallback` | `loop.Middleware`, model and tool decorators and a retrying `memory.Embedder` | none |
| `github.com/nostosx/agentx-contrib/pgvector` | `pgvector` | PostgreSQL `rag.VectorStore`, `memory.Store` and database-side `memory.Retriever`, with namespace isolation | library uses `database/sql`; integration tests use pgx |
| `github.com/nostosx/agentx-contrib/document` | `document` | PDF, DOCX, XLSX and image OCR `rag.Parser` implementations | Excelize; application-installed Poppler/Tesseract executables |
| `github.com/nostosx/agentx-contrib/s3` | `s3` | Paginated S3 document `rag.Loader` using application-provided parsers | AWS SDK for Go v2 S3 |

The table lists each module's direct external dependencies, omitting the
shared agentx dependency. Every module also depends on kitx through agentx;
"none" means no additional direct external dependency.

## Installation

Web/browser, cloud loaders and source writes require agentx v1.4.0: web/browser v1.0.0, loader v1.0.1 and retrieval v1.1.0. Their HTTP foundation is servex-contrib/transport/httpx v1.1.0.

The earlier 2026-10-31 releases use agentx v1.3.0: provider v1.3.0, middleware v1.2.0,
filestore/document v1.1.0, and artifact/debug/embedding/retrieval/search v1.0.0.
Existing otel, sqlstore, pgvector and s3 retain their independent published versions.
All resolve public dependencies without replacements. Install only the modules
your application needs; the repository root is not a Go module.

```sh
go get github.com/nostosx/agentx@v1.3.0 github.com/nostosx/agentx-contrib/provider@v1.3.0
go get github.com/nostosx/agentx-contrib/filestore@v1.1.0     # file stores
go get github.com/nostosx/agentx-contrib/sqlstore@v1.1.0      # SQL stores
go get github.com/nostosx/agentx-contrib/middleware@v1.2.0    # logging and retries
go get github.com/nostosx/agentx-contrib/otel@v1.1.0          # OpenTelemetry
go get github.com/nostosx/agentx-contrib/document@v1.1.0
go get github.com/nostosx/agentx-contrib/pgvector@v1.0.0
go get github.com/nostosx/agentx-contrib/s3@v1.0.0
go get github.com/nostosx/agentx-contrib/artifact@v1.0.0
go get github.com/nostosx/agentx-contrib/embedding@v1.0.0
go get github.com/nostosx/agentx-contrib/search@v1.0.0
go get github.com/nostosx/agentx-contrib/retrieval@v1.1.0
go get github.com/nostosx/agentx-contrib/web@v1.0.0
go get github.com/nostosx/agentx-contrib/browser@v1.0.0
go get github.com/nostosx/agentx-contrib/loader@v1.0.1
go get github.com/nostosx/agentx-contrib/debug@v1.0.0
```

Each module is versioned on its own, with tags of the form
`<dir>/vX.Y.Z`, such as `provider/v1.0.0`. A root tag does not release
these nested modules.

## Quick start

This file-store example needs no model API or database server. In a new
application directory, run `go mod init example.com/agentx-files` and
`go get github.com/nostosx/agentx-contrib/filestore@v1.1.0`. Save this as
`main.go`, then run `go mod tidy` followed by `go run .`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nostosx/agentx-contrib/filestore"
	"github.com/nostosx/agentx/model"
	"github.com/nostosx/agentx/session"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := filestore.Sessions(".agentx/sessions")
	if err != nil {
		log.Fatal(err)
	}
	sess := session.New("demo")
	sess.Append(model.NewMessage(model.RoleUser, model.TextPart("Hello")))
	if err := store.Save(ctx, sess); err != nil {
		log.Fatal(err)
	}
	saved, err := store.Get(ctx, sess.ID())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(saved.ID(), len(saved.Messages()))
}
```

The output is `demo 1`; the session remains in `.agentx/sessions` between
processes. That directory may contain sensitive conversation data: keep it
private and out of version control. The feature examples below are fragments
that omit imports and reuse application values. For a complete model call,
see the [agentx quick start](https://github.com/nostosx/agentx#quick-start).

## Providers

| Package | API | Environment variables |
| --- | --- | --- |
| `provider/anthropic` | Messages API (prompt caching, adaptive and extended thinking, server-side fallbacks) | `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` |
| `provider/openai` | Responses API (stateless, encrypted reasoning); Chat Completions with `openai.WithChatCompletions()` for compatible services | `OPENAI_API_KEY`, `OPENAI_BASE_URL` |
| `provider/gemini` | Gemini `generateContent` (thinking, thought signatures, function calling) | `GEMINI_API_KEY` or `GOOGLE_API_KEY`, `GEMINI_BASE_URL` |
| `provider/bedrock` | Amazon Bedrock Converse and ConverseStream (SigV4 or API key, inference profiles, guardrails) | `AWS_REGION`, `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` (`AWS_SESSION_TOKEN`) or `AWS_BEARER_TOKEN_BEDROCK` |

The providers call the APIs directly over `net/http`, with no SDK. Every
provider supports regular calls, streaming, tool calls, images, documents
and structured output; see [Multimodal content](#multimodal-content) for
media. For request fields specific to one provider, use
`agent.WithProviderOptions` with the adapter's name (`"openai"`,
`"anthropic"`, `"gemini"` or `"bedrock"`); each adapter merges only its own
options into the request body. `agent.WithSeed` is sent by OpenAI Chat
Completions and Gemini and ignored by the others.

Ordinary provider tests use local servers that mimic the APIs and do not
verify live compatibility. The [acceptance command](provider/README.md)
explicitly opts into live checks and records individual results; absent
credentials or disabled live access returns unverified with a nonzero exit.
Ordinary tests and CI never enable paid provider requests.

```go
m, err := anthropic.New() // reads ANTHROPIC_API_KEY
if err != nil {
	log.Fatal(err)
}
a := agent.MustNew("assistant", agent.WithModel(m))
```

**OpenAI-compatible services.** Presets set the base URL, key variable,
default model and the request fields each service lacks, and use Chat
Completions. Other options override them, in any order.

| Preset | Base URL | Key | Default model | Adjustments |
| --- | --- | --- | --- | --- |
| `openai.Ollama()` | `http://localhost:11434/v1` | `OLLAMA_API_KEY` (optional) | `gemma4:e2b` | `max_tokens`; no `tool_choice` |
| `openai.DeepSeek()` | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | `deepseek-flash` | `max_tokens`; JSON mode; reasoning sent back |
| `openai.OpenRouter()` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openrouter/auto` | none |
| `openai.Groq()` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `openai/gpt-oss-120b` | no message names |
| `openai.XAI()` | `https://api.x.ai/v1` | `XAI_API_KEY` | `grok-4.7` | none |
| `openai.Mistral()` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | `mistral-large-latest` | `max_tokens`; no `stream_options` or names |
| `openai.Together()` | `https://api.together.ai/v1` | `TOGETHER_API_KEY` | `openai/gpt-oss-120b` | `max_tokens`; no `stream_options` |
| `openai.Fireworks()` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `accounts/fireworks/models/kimi-k2-instruct-0905` | none |
| `openai.VLLM()` | `http://localhost:8000/v1` | `VLLM_API_KEY` (optional) | set with `WithModel` | none |
| `openai.LMStudio()` | `http://localhost:1234/v1` | none | set with `WithModel` | `max_tokens` |

OpenRouter's optional attribution headers can be added with
`openai.WithHeader("HTTP-Referer", url)` and
`openai.WithHeader("X-OpenRouter-Title", name)`.

**Cloud platforms and tokens.** Each provider has `WithTokenSource`, which
sends a bearer token fetched for every request; getting tokens, for example
with `golang.org/x/oauth2/google` or `azidentity` in your module, is up to
you. Environment API keys are then not sent.

- `openai.WithAzure(endpoint)` uses the Azure OpenAI v1 API
  (`{endpoint}/openai/v1`) with the deployment as `WithModel` and the key in
  `api-key` (`AZURE_OPENAI_API_KEY`), or an Entra ID token.
  `openai.WithAzureDeployment(endpoint, deployment, apiVersion)` uses the
  dated `/openai/deployments/{deployment}` API with `api-version`.
- `gemini.WithVertex(project, location)` and
  `anthropic.WithVertex(project, region)` call Vertex AI
  (`global`, `us`/`eu` multi-region or a region). Claude on Vertex AI gets
  the model in the URL (`rawPredict`, `streamRawPredict`), `anthropic_version`
  in the body and no server-side fallbacks.
- `bedrock.WithCredentialsProvider` supplies credentials from other sources,
  such as profiles, IAM roles or SSO, and is called for every request.

```go
ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform") // golang.org/x/oauth2/google
if err != nil {
	log.Fatal(err)
}
m, _ := anthropic.New(anthropic.WithVertex("my-project", "global"),
	anthropic.WithTokenSource(func(context.Context) (string, error) {
		t, err := ts.Token()
		if err != nil {
			return "", err
		}
		return t.AccessToken, nil
	}))
```

- **Encrypted reasoning.** Stateless Responses requests return reasoning
  with its encrypted content by default, so `provider/openai` sends no
  `include`. `openai.WithEncryptedReasoning(true)` adds the legacy
  `reasoning.encrypted_content` value, and an `include` passed in the
  `"openai"` provider options is merged.
- **Freeform tools.** `provider/openai` sends tools with a grammar
  (`tool.Freeform`), such as `apply_patch`, to the Responses API as custom
  tools, whose calls carry plain text. `openai.WithFreeformTools(false)`
  sends them as functions for compatible services without custom tools;
  Chat Completions and other providers always do.
- **Reasoning effort.** `provider/anthropic` sends the thinking type each
  Claude model accepts: adaptive on Opus 4.7 and later and generation 5,
  a budget derived from the effort on 4.5 and earlier, and `none` as
  disabled thinking where allowed. `provider/gemini` maps the effort to the
  nearest thinking level the model accepts, or to a budget within the
  model's range on Gemini 2.5. `provider/bedrock` applies the Claude rules
  through `additionalModelRequestFields` and maps the effort to Nova 2's
  `reasoningConfig`. OpenAI effort values depend on the model.
- **Sampling parameters.** `temperature` and `top_p` are dropped where the
  API would reject them: on Claude Opus 4.7 and later, generation-5 Claude
  and Fable and Mythos models; on older Claude with thinking (temperature,
  and top_p outside 0.95–1); and with a reasoning effort other than `none`
  on the Responses API, or on Chat Completions against OpenAI's own API
  (`api.openai.com`, `*.openai.azure.com`). Other compatible services
  receive them as set.
- **Thinking blocks.** Editing earlier history invalidates Anthropic
  thinking replayed after it on models with the thinking prefix check;
  `provider/anthropic` asks the API to drop those blocks, so requests
  succeed and the model thinks again. `anthropic.WithoutThinkingBlockDrop()`
  turns this off.

### Multimodal content

agentx messages and tool results carry images, documents, audio and video
(see the agentx README). The providers take them as follows:

| Content | OpenAI Responses | Chat Completions | Anthropic | Gemini | Bedrock |
| --- | --- | --- | --- | --- | --- |
| Images | data, URL, file ID | data, URL | data, URL, file ID | data, URL | data, S3 |
| Documents | PDF, text, Office: data, URL, file ID | PDF data or file ID; text as text | PDF, text: data, URL, file ID | data, URL | PDF, text, CSV, HTML, Office: data, S3 |
| Audio | no | WAV, MP3 data | no | data, URL | data, S3 |
| Video | no | no | no | data, URL, YouTube | data, S3 |
| In tool results | images, files | in a user message after the results | images, documents | Gemini 3: images, PDF, text; others after the response | images, documents, video; audio after the result |

- **Errors.** Content a provider or model cannot take fails before the
  request with a permanent error, such as audio on Anthropic or a document
  URL on Chat Completions.
- **Output.** Gemini image and speech models return image and audio parts,
  Chat Completions audio models audio parts with the transcript in `Text`,
  and Bedrock image blocks image parts.

### Foundations and domain adapters

Core owns AI contracts: artifact.Store, memory.Embedder, model.Model and
imagegen. Contrib owns their implementations. Shared infrastructure comes
from servex and servex-contrib: [artifact storage](artifact/README.md) uses
its S3 Store; [cached embeddings](embedding/README.md) use its Cache contract,
Loader and an injected codec. The application owns clients, credentials,
backends and lifetimes. Agentx core takes no servex dependency.

`filestore.Artifacts(dir)` provides the file artifact backend.
`middleware/fallback.New` composes model targets and never switches after
stream output. See [fallback semantics](middleware/fallback/README.md).
These modules use published dependencies; no sibling checkout or local replacement is required.

### Retrieval, search and debugging

[retrieval](retrieval/README.md) queries Qdrant and Elasticsearch through core
`rag.Retriever`; applications manage indexes, without weakening VectorStore
atomic replacement. [search](search/README.md) implements core `search.Searcher`
for Tavily/Serper and can be registered as an Agent tool.
[debug](debug/README.md) reuses telemetry/eval for bounded traces, evaluation
summaries and baseline comparison, with mandatory application authorization.
Compose existing servex session/auth and profile facilities at application level.
These additions are available in the 2026-10-31 module releases listed below.

### Image generation

`openai.NewImageGenerator` and `gemini.NewImageGenerator` implement the core
`imagegen` generation, editing and streaming interfaces. OpenAI uses the Images
API with GPT Image 2.5 Sunburst (default) or Flare and supports reference images,
masks and markup. Gemini uses generateContent with Nano Banana 2.1 by default,
reference-image edits and ratio/resolution settings; unsupported typed options
fail explicitly. Both reuse their existing authentication/HTTP infrastructure
and return decoded image parts, request IDs and available detailed usage.

`imagegen.Tool` makes the same client callable by an Agent. See
[generation, editing, streaming, Agent and live-acceptance examples](provider/IMAGES.md).
Use core v1.3.0 and provider v1.3.0 or later for image generation.

### Embedders

Provider v1.2.0 also supports Vertex AI predict and Bedrock Titan
embeddings, plus a Cohere `rag.Reranker`. See [Provider README](provider/README.md)
for options and the bounded, explicitly enabled live acceptance command.

`openai.NewEmbedder` (`/embeddings`, including compatible services such as
`openai.Ollama()`) and `gemini.NewEmbedder` (`batchEmbedContents`)
implement `memory.Embedder` for agentx's `memory` and `rag` packages. They
take the options of `New`; `WithModel` selects the embedding model, and
`WithDimensions` and `WithBatchSize` are optional. Gemini can embed queries
and stored entries for different tasks. Provider v1.2.0 supports
Vertex AI embeddings too. `retry.Embedder` retries them like model calls:

```go
docs, _ := gemini.NewEmbedder(gemini.WithTaskType(gemini.TaskRetrievalDocument))
queries, _ := gemini.NewEmbedder(gemini.WithTaskType(gemini.TaskRetrievalQuery))
policy := retry.Policy{}
mem := memory.New(store,
	memory.WithEmbedder(retry.Embedder(policy, docs)),
	memory.WithQueryEmbedder(retry.Embedder(policy, queries)))
```

## Storage

The [pgvector backend](pgvector/README.md) provides namespace-filtered
database-side exact cosine retrieval for RAG and long-term memory. It owns
separate tables, never imports sqlstore, and does not install the vector
extension or create approximate indexes automatically. See also the
[document parsers](document/README.md), [S3 loader](s3/README.md) and
[Provider acceptance and reranking](provider/README.md).

### File storage

`filestore` keeps state in JSON files, replacing each file through a
temporary file renamed into place. Replacement is atomic on Unix; other
platforms follow `os.Rename` guarantees. New files use mode 0600 and new
directories mode 0700, subject to platform permissions; existing directories
keep their permissions. Use trusted paths on a local filesystem. The stores
have different concurrency boundaries:

- Sessions serialize revision checks only within one store instance. Reuse
  that instance, and coordinate concurrent deletes externally. Separate
  instances and processes need external coordination or `sqlstore` to prevent
  lost updates; file-level locking does not make revision checks transactional.
- Session and checkpoint reads, replacements and deletes use persistent
  `.lock` sidecars. Keep them in place while any store is active.
- Memory coordinates operations across processes as described below.
- Vectors keep an instance-local snapshot; MCP tokens serialize updates
  within one instance. Give each path one store owner rather than sharing it
  between independent writers.

| Constructor | Implements |
| --- | --- |
| `filestore.Sessions(dir)` | `session.Store`; `Save` detects conflicts within the coordination limits above |
| `filestore.Checkpoints(dir)` | `loop.Checkpointer` |
| `filestore.WorkflowCheckpoints(dir)` | `workflow.Checkpointer`; `IDs` lists the stored runs |
| `filestore.Memory(path)` | `memory.Store` and `memory.ScopedDeleter`, in one file |
| `filestore.Vectors(dir)` | `rag.VectorStore` and `rag.Revisioner`, one file per source, searched in memory |
| `filestore.MCPTokens(path)` | `mcp.TokenStore` for MCP OAuth tokens and client registrations |

Memory operations reload the current file under a process-shared OS lock,
including the scope check during deletion. The persistent `path + ".lock"`
sidecar must remain in place while stores are in use. Every writer must
use `filestore.Memory`; use a local filesystem with file locks and atomic
replacement. Locking supports Linux, macOS, BSD, illumos and Windows;
other platforms return an error from memory operations. Reads report file
errors instead of serving an older cached snapshot, and waiting for a
lock honors context cancellation.

```go
cp, _ := filestore.Checkpoints(".agentx/checkpoints")
sessions, _ := filestore.Sessions(".agentx/sessions")
a := agent.MustNew("ops", agent.WithModel(m), agent.WithCheckpointer(cp))
runner := agent.NewRunner(a, sessions)

store, _ := filestore.Memory(".agentx/memory.json")
mem := memory.New(store)
vectors, _ := filestore.Vectors(".agentx/rag")
ix := rag.NewIndex(rag.Config{Store: vectors, Embedder: emb})
cfg := &mcp.OAuthConfig{Store: filestore.MCPTokens(".agentx/mcp-tokens.json")}
```

### SQL storage

Since sqlstore v1.1.0, `sqlstore.MCPTokens(db, namespace, aead, opts...)` stores MCP
OAuth tokens and client registrations with required authenticated encryption
and per-user/trust-domain namespaces. The application coordinates refreshes
across replicas. See [SQL store README](sqlstore/README.md) for key ownership.

`sqlstore` keeps sessions, run checkpoints, workflow checkpoints and
memories in SQLite, PostgreSQL or MySQL (and compatible servers such as
MariaDB) through `database/sql`. It imports no driver: open the database
with yours and pass the handle, a `*sql.DB`, `*sql.Conn` or `*sql.Tx`.

```go
db, _ := sql.Open("pgx", os.Getenv("DATABASE_URL")) // import _ "github.com/jackc/pgx/v5/stdlib"
opts := []sqlstore.Option{sqlstore.WithDialect(sqlstore.PostgreSQL)}
if err := sqlstore.Migrate(ctx, db, opts...); err != nil { // idempotent
	return err
}
mem := memory.New(sqlstore.Memory(db, opts...))
a := agent.MustNew("assistant", agent.WithModel(m), agent.WithMemory(mem, "user-42", 3),
	agent.WithCheckpointer(sqlstore.Checkpoints(db, opts...)))
runner := agent.NewRunner(a, sqlstore.Sessions(db, opts...))
flow := g.MustCompile( // g is a *workflow.Graph
	workflow.WithCheckpointer(sqlstore.WorkflowCheckpoints(db, opts...)))
```

- **Tables.** Each store has one table, prefixed `agentx_` unless you set
  `sqlstore.WithPrefix`: `sessions`, `checkpoints`,
  `workflow_checkpoints` and `memories`. Values are JSON documents. Keys
  compare exactly (`VARBINARY` on MySQL), timestamps are fixed-width UTC
  strings and embeddings are little-endian float32 blobs.
- **Conflicts.** `Save` updates a session only at the revision it was
  loaded at, across processes, and otherwise returns `session.ErrConflict`.
- **Transactions.** Stores do not open transactions. Individual mutating
  statements are atomic, but an operation such as `SessionStore.Save` can
  execute several statements. Pass a `*sql.Tx` when those statements or
  several store operations must share a transaction; the caller owns commit,
  rollback and the database handle's lifetime.
- **SQLite.** Set a busy timeout, for example the
  `_pragma=busy_timeout(5000)` DSN parameter of `modernc.org/sqlite`, so
  that concurrent writers wait.

Both packages pass the conformance suites of agentx: `session/sessiontest`,
`loop/checkpointtest`, `workflow/checkpointtest`, `memory/memorytest` and,
for `filestore.Vectors`, `rag/ragtest`.

## Middleware

`logging` logs each model and tool call with `log/slog`, with the run ID,
session ID, agent name and turn. `retry` retries failed model calls with
exponential backoff and jitter. Register tracing and metrics before
retries, so a model call's span and duration cover its retries, which are
recorded as `agentx.retry` span events:

```go
a := agent.MustNew("assistant",
	agent.WithModel(m),
	agent.WithMiddleware(otel.Middleware(tracerProvider, meterProvider)...),
	agent.WithMiddleware(
		logging.New(slog.Default()),
		retry.New(retry.Policy{MaxAttempts: 5}),
	),
)
```

`retry.Policy` defaults to 3 attempts, backoff from 500 ms up to 30 s with
multiplier 2 and 20% jitter, and retries only transient failures:

- **Retried:** retryable API errors (408, 409, 429 unless a quota is
  exhausted, and 5xx) and transport failures: refused, reset or dropped
  connections, HTTP/2 stream resets, HTTP client timeouts and connections
  lost while reading the response.
- **Not retried:** invalid requests, undecodable responses, certificate and
  TLS errors, unknown hosts, and a cancelled or expired caller context.
- **`Retry-After`:** the server's hint is honored; a hint longer than
  `MaxBackoff` ends the retries.
- **Streams** are retried only if they failed before yielding any event.

`Policy.Retryable` replaces the decision, which defaults to
`model.IsRetryable`. For model calls outside the agent loop, such as the
model of an `actx.Summarize` strategy, wrap the model with the decorators:
`middleware.Wrap(m, logging.Model(log), retry.Model(policy))`.
`logging.Tools(log)` is the `tool.Middleware` that logs tool calls.

## OpenTelemetry

`otel` adapts agentx's tracing and metrics, which follow the
[OpenTelemetry GenAI semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai),
to OpenTelemetry providers. `otel.Tracer` and `otel.Meter` return a
`telemetry.Tracer` and `telemetry.Meter`, and `otel.Middleware` returns
`middleware.Tracing` and `middleware.Metrics` backed by them. A nil provider
uses the global one.

```go
tp := sdktrace.NewTracerProvider() // go.opentelemetry.io/otel/sdk/trace
mp := sdkmetric.NewMeterProvider() // go.opentelemetry.io/otel/sdk/metric
lp := sdklog.NewLoggerProvider()   // go.opentelemetry.io/otel/sdk/log
defer tp.Shutdown(ctx)
defer mp.Shutdown(ctx)
defer lp.Shutdown(ctx)
a := agent.MustNew("assistant",
	agent.WithModel(m),
	agent.WithMiddleware(otel.Middleware(tp, mp, otel.WithLoggerProvider(lp))...),
)
```

- **Events.** With `otel.WithLoggerProvider`, model calls also emit the
  `gen_ai.client.inference.operation.details` and
  `gen_ai.client.operation.exception` events as log records.
- **Content.** `otel.RecordContent()` records the system prompt, messages
  and tool arguments and results, which may contain sensitive data.
- **Options.** `otel.WithTraceOptions` and `otel.WithMetricsOptions` pass
  options of agentx's `middleware` package, such as
  `middleware.LegacyMetrics()`.
- **Scope.** The instrumentation scope is `otel.ScopeName`,
  `github.com/nostosx/agentx-contrib/otel`, with `agentx.Version` as its
  version.

## Examples

Runnable programs live in the separate
[agentx-examples](https://github.com/nostosx/agentx-examples) project.
It composes the core with provider and file-store modules. See its README for
setup, provider environment variables and commands under `cmd/`.

## Development

Use Go 1.27.3, Task v3.53.1 and Bash. Clone this repository on its own;
the published agentx and kitx versions download through the Go module proxy. The root is not a
module, and no `go.work` is committed. All Task targets set `GOWORK=off`.

```sh
task check        # format, modernizer and tidy checks, vet, build, shuffled race tests
task lint         # golangci-lint, including integration builds
task vulncheck    # reachable vulnerability scans, including integration builds
task test         # module tests and tooling regression tests
task test RACE=true  # include the race detector
task test COVER=true  # race tests plus per-module coverage.out
task test INTEGRATION=true MODULES=sqlstore  # requires PostgreSQL and MySQL DSNs
task check MODULES=provider  # select a module, or a quoted space-separated list
```

`task --list` lists all targets. `check` rejects formatting, modernizer and tidy
differences without applying them. Use `task fmt`, `task fix` or `task tidy`
explicitly, then review the changes. Modernizer, vet, build, lint and
vulnerability checks cover both default and integration builds. CI runs
`task check` per module on Linux, macOS and Windows, with independent lint,
vulnerability and dual-database integration jobs.

The ordinary `sqlstore` tests use SQLite and need no external service.
With `integration` tags, tests also use PostgreSQL and MySQL when their DSNs
are set; directly running tagged Go tests skips an unset backend. The
`task test INTEGRATION=true MODULES=sqlstore` gate requires both DSNs and rejects skipped or missing
conformance suites. Use disposable databases: the tests create and drop tables.

```sh
export AGENTX_IT_POSTGRES_DSN='postgres://agentx:agentx@localhost:5432/agentx_test'
export AGENTX_IT_MYSQL_DSN='agentx:agentx@tcp(localhost:3306)/agentx_test'
task test INTEGRATION=true MODULES=sqlstore
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for tool setup and contribution
requirements, [SECURITY.md](SECURITY.md) for vulnerability reports and
[RELEASING.md](RELEASING.md) for independent versions, core dependency updates
and release gates.

## License

[MIT](LICENSE)
