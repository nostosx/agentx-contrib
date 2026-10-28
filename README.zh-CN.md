# agentx-contrib

[English](README.md) | 简体中文

agentx-contrib 存放 [agentx](https://github.com/nostosx/agentx) 中需要第三方
依赖或特定于某个服务的实现：模型 Provider、文件与 SQL 存储、OpenTelemetry、
日志和重试。agentx 模块本身只依赖 Go 标准库和 kitx，只保留各项契约以及每项契约的一个
内存参考实现；框架本身见
[agentx README](https://github.com/nostosx/agentx/blob/main/README.zh-CN.md)。

每个目录都是独立模块，程序只会依赖它导入的部分。

> 需要 Go 1.27.3 或更高版本。版本变更记录在
> [CHANGELOG.md](CHANGELOG.md)。

## 模块

| 模块 | 包 | 实现 | 第三方依赖 |
| --- | --- | --- | --- |
| `github.com/nostosx/agentx-contrib/provider` | `provider/openai`、`provider/anthropic`、`provider/gemini`、`provider/bedrock`、`provider/cohere` | `model.Model`；`memory.Embedder`（OpenAI、Gemini/Vertex、Bedrock Titan）；`rag.Reranker`（Cohere） | `github.com/nostosx/kitx/dataurl`、`github.com/nostosx/kitx/utf8text` |
| `github.com/nostosx/agentx-contrib/otel` | `otel` | 基于 OpenTelemetry 的 `telemetry.Tracer` 和 `telemetry.Meter` | `go.opentelemetry.io/otel` 及其 `trace`、`metric` 和 `log` API（SDK 仅用于测试） |
| `github.com/nostosx/agentx-contrib/sqlstore` | `sqlstore` | SQLite、PostgreSQL 和 MySQL 上的 `session.Store`、`loop.Checkpointer`、`workflow.Checkpointer`、`memory.Store` 和加密 `mcp.TokenStore` | 包本身无；测试使用 `modernc.org/sqlite`、`github.com/jackc/pgx/v5` 和 `github.com/go-sql-driver/mysql` |
| `github.com/nostosx/agentx-contrib/filestore` | `filestore` | 基于 JSON 文件的 `session.Store`、`loop.Checkpointer`、`workflow.Checkpointer`、`memory.Store`、`rag.VectorStore` 和 `mcp.TokenStore` | `github.com/nostosx/kitx/atomicfile` |
| `github.com/nostosx/agentx-contrib/middleware` | `middleware/logging`、`middleware/retry` | `loop.Middleware`、模型和工具装饰器，以及带重试的 `memory.Embedder` | 无 |
| `github.com/nostosx/agentx-contrib/pgvector` | `pgvector` | PostgreSQL 上的 `rag.VectorStore`、`memory.Store` 和数据库端 `memory.Retriever`，支持命名空间隔离 | 库代码仅使用 `database/sql`；集成测试使用 pgx |
| `github.com/nostosx/agentx-contrib/document` | `document` | PDF、DOCX 的 `rag.Parser`，保留来源与页码等元数据 | PDF 需要应用安装 `pdftotext`；DOCX 使用标准库 |
| `github.com/nostosx/agentx-contrib/s3` | `s3` | 分页加载 S3 文档的 `rag.Loader`，复用应用配置的 Parser | AWS SDK for Go v2 S3 |

上表列出各模块直接使用的外部依赖，不重复列出共同依赖的 agentx。
所有模块都会通过 agentx 间接依赖 kitx；“无”表示没有额外的直接外部依赖。

## 安装

2026-10-28 发布 provider v1.2.0、sqlstore v1.1.0，以及 document、pgvector、s3
的首个 v1.0.0，均需要 agentx v1.2.1。middleware、otel 保持 v1.1.0，filestore
保持 v1.0.0。各模块独立发布，依赖均通过公开代理解析，无需本地替换。
只安装应用需要的模块；本仓库根目录不是 Go 模块。

在应用的 Go 模块内执行这些命令。新建应用时，先创建目录并在其中运行
`go mod init example.com/agentx-app`。本仓库根目录不是可安装的模块。

```sh
go get github.com/nostosx/agentx@v1.2.1 github.com/nostosx/agentx-contrib/provider@v1.2.0
go get github.com/nostosx/agentx-contrib/filestore@v1.0.0     # 文件存储
go get github.com/nostosx/agentx-contrib/sqlstore@v1.1.0      # SQL 存储
go get github.com/nostosx/agentx-contrib/middleware@v1.1.0    # 日志和重试
go get github.com/nostosx/agentx-contrib/otel@v1.1.0          # OpenTelemetry
go get github.com/nostosx/agentx-contrib/document@v1.0.0
go get github.com/nostosx/agentx-contrib/pgvector@v1.0.0
go get github.com/nostosx/agentx-contrib/s3@v1.0.0
```

每个模块单独发布版本，标签形如 `<dir>/vX.Y.Z`，例如 `provider/v1.0.0`。
根目录标签不会发布这些嵌套模块。

## 快速开始

这个文件存储示例不需要模型 API 或数据库服务。在新的应用目录里运行
`go mod init example.com/agentx-files` 和
`go get github.com/nostosx/agentx-contrib/filestore@v1.0.0`，把下面的程序保存为
`main.go`，再依次运行 `go mod tidy`、`go run .`：

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

输出为 `demo 1`，会话会保存在 `.agentx/sessions`，退出程序后仍然存在。
该目录可能包含敏感对话，应保持私有并排除在版本控制之外。下面的功能示例是
省略 import、复用应用变量的代码片段。完整模型调用示例见
[agentx 快速开始](https://github.com/nostosx/agentx/blob/main/README.zh-CN.md#快速开始)。

## Provider

| 包 | API | 环境变量 |
| --- | --- | --- |
| `provider/anthropic` | Messages API（提示缓存、自适应和扩展思考、服务端回退） | `ANTHROPIC_API_KEY`、`ANTHROPIC_BASE_URL` |
| `provider/openai` | Responses API（无状态，加密推理）；兼容服务可用 `openai.WithChatCompletions()` 切换到 Chat Completions | `OPENAI_API_KEY`、`OPENAI_BASE_URL` |
| `provider/gemini` | Gemini `generateContent`（思考、思考签名、函数调用） | `GEMINI_API_KEY` 或 `GOOGLE_API_KEY`、`GEMINI_BASE_URL` |
| `provider/bedrock` | Amazon Bedrock Converse 和 ConverseStream（SigV4 签名或 API 密钥、推理配置文件、护栏） | `AWS_REGION`、`AWS_ACCESS_KEY_ID` 和 `AWS_SECRET_ACCESS_KEY`（`AWS_SESSION_TOKEN`）或 `AWS_BEARER_TOKEN_BEDROCK` |

各 Provider 不使用 SDK，直接通过 `net/http` 调用 API。所有 Provider 都支持
普通调用、流式、工具调用、图片、文档和结构化输出；媒体见
[多模态内容](#多模态内容)。某个 Provider 特有的请求字段可以通过
`agent.WithProviderOptions` 按适配器名（`"openai"`、`"anthropic"`、`"gemini"` 或
`"bedrock"`）传入，每个适配器只把自己的选项合并进请求体。`agent.WithSeed` 由 OpenAI
Chat Completions 和 Gemini 发送，其他适配器忽略。

普通 Provider 测试使用本地模拟 API，不能证明真实 API 的兼容性。
[Provider 验收入口](provider/README.md) 可显式启用真实 API 检查，记录模型、时间及逐项结果；
未启用或缺少凭据时返回 `unverified` 并非零退出。普通测试和 CI 不调用付费 API。

```go
m, err := anthropic.New() // 读取 ANTHROPIC_API_KEY
if err != nil {
	log.Fatal(err)
}
a := agent.MustNew("assistant", agent.WithModel(m))
```

**OpenAI 兼容服务**：预设会设置 Base URL、密钥变量、默认模型，并去掉各服务
不支持的请求字段，使用 Chat Completions。其他选项会覆盖预设，与顺序无关。

| 预设 | Base URL | 密钥 | 默认模型 | 调整 |
| --- | --- | --- | --- | --- |
| `openai.Ollama()` | `http://localhost:11434/v1` | `OLLAMA_API_KEY`（可选） | `gemma4:e2b` | `max_tokens`；不发送 `tool_choice` |
| `openai.DeepSeek()` | `https://api.deepseek.com` | `DEEPSEEK_API_KEY` | `deepseek-flash` | `max_tokens`；JSON 模式；回传推理 |
| `openai.OpenRouter()` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openrouter/auto` | 无 |
| `openai.Groq()` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `openai/gpt-oss-120b` | 不发送消息名称 |
| `openai.XAI()` | `https://api.x.ai/v1` | `XAI_API_KEY` | `grok-4.7` | 无 |
| `openai.Mistral()` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | `mistral-large-latest` | `max_tokens`；不发送 `stream_options` 和名称 |
| `openai.Together()` | `https://api.together.ai/v1` | `TOGETHER_API_KEY` | `openai/gpt-oss-120b` | `max_tokens`；不发送 `stream_options` |
| `openai.Fireworks()` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` | `accounts/fireworks/models/kimi-k2-instruct-0905` | 无 |
| `openai.VLLM()` | `http://localhost:8000/v1` | `VLLM_API_KEY`（可选） | 用 `WithModel` 设置 | 无 |
| `openai.LMStudio()` | `http://localhost:1234/v1` | 无 | 用 `WithModel` 设置 | `max_tokens` |

OpenRouter 可选的归属请求头可以用 `openai.WithHeader("HTTP-Referer", url)` 和
`openai.WithHeader("X-OpenRouter-Title", name)` 添加。

**云平台与令牌**：每个 Provider 都有 `WithTokenSource`，每次请求获取一个令牌并
作为 Bearer 令牌发送；令牌如何获取由你决定，例如在你的模块中使用
`golang.org/x/oauth2/google` 或 `azidentity`。此时不会发送环境变量中的 API 密钥。

- `openai.WithAzure(endpoint)` 使用 Azure OpenAI v1 API（`{endpoint}/openai/v1`），
  部署名通过 `WithModel` 设置，密钥放在 `api-key` 中（`AZURE_OPENAI_API_KEY`），
  也可以使用 Entra ID 令牌。`openai.WithAzureDeployment(endpoint, deployment, apiVersion)`
  使用带 `api-version` 的旧版 `/openai/deployments/{deployment}` API。
- `gemini.WithVertex(project, location)` 和 `anthropic.WithVertex(project, region)`
  调用 Vertex AI（`global`、`us`/`eu` 多区域或单个区域）。Vertex AI 上的 Claude
  把模型放在 URL 中（`rawPredict`、`streamRawPredict`），在请求体中发送
  `anthropic_version`，并且不使用服务端回退。
- `bedrock.WithCredentialsProvider` 从其他来源（例如 profile、IAM 角色或 SSO）
  提供凭据，每次请求都会调用它。

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

- **加密推理**：无状态的 Responses 请求默认就会返回带加密内容的推理，所以
  `provider/openai` 不发送 `include`。`openai.WithEncryptedReasoning(true)`
  会加上旧的 `reasoning.encrypted_content` 值，通过 `"openai"` Provider 选项传入的
  `include` 会被合并。
- **自由格式工具**：`provider/openai` 把带语法的工具（`tool.Freeform`），例如
  `apply_patch`，作为自定义工具发送给 Responses API，其调用携带纯文本。
  `openai.WithFreeformTools(false)` 会改为以函数形式发送，适用于不支持自定义
  工具的兼容服务；Chat Completions 和其他 Provider 总是以函数形式发送。
- **推理强度**：`provider/anthropic` 按各 Claude 模型接受的思考类型发送：
  Opus 4.7 及之后和第 5 代使用自适应思考，4.5 及更早的模型使用由推理强度推导
  的预算，`none` 在允许的模型上关闭思考。`provider/gemini` 把推理强度映射到
  模型接受的最接近的思考级别，Gemini 2.5 则映射到模型范围内的预算。
  `provider/bedrock` 通过 `additionalModelRequestFields` 应用 Claude 的规则，
  并把推理强度映射到 Nova 2 的 `reasoningConfig`。OpenAI 的推理强度取值取决于
  模型。
- **采样参数**：在 API 会拒绝的情况下去掉 `temperature` 和 `top_p`：Claude
  Opus 4.7 及之后、第 5 代 Claude 以及 Fable 和 Mythos 模型；开启思考的较早
  Claude 模型（去掉 temperature，以及 0.95–1 范围之外的 top_p）；推理强度不为
  `none` 时的 Responses API，以及连接 OpenAI 官方 API（`api.openai.com`、
  `*.openai.azure.com`）的 Chat Completions。其他兼容服务按设置原样接收。
- **思考块**：在启用思考前缀检查的模型上，编辑较早的历史会让其后回放的
  Anthropic 思考失效；`provider/anthropic` 会让 API 丢弃这些块，请求因此成功，
  模型会重新思考。`anthropic.WithoutThinkingBlockDrop()` 关闭这一行为。

### 多模态内容

agentx 的消息和工具结果可以携带图片、文档、音频和视频（见 agentx README）。
各 Provider 的支持情况如下：

| 内容 | OpenAI Responses | Chat Completions | Anthropic | Gemini | Bedrock |
| --- | --- | --- | --- | --- | --- |
| 图片 | 数据、URL、文件 ID | 数据、URL | 数据、URL、文件 ID | 数据、URL | 数据、S3 |
| 文档 | PDF、文本、Office：数据、URL、文件 ID | PDF 数据或文件 ID；文本按文本发送 | PDF、文本：数据、URL、文件 ID | 数据、URL | PDF、文本、CSV、HTML、Office：数据、S3 |
| 音频 | 不支持 | WAV、MP3 数据 | 不支持 | 数据、URL | 数据、S3 |
| 视频 | 不支持 | 不支持 | 不支持 | 数据、URL、YouTube | 数据、S3 |
| 工具结果中 | 图片、文件 | 放在结果之后的用户消息里 | 图片、文档 | Gemini 3：图片、PDF、文本；其他模型放在响应之后 | 图片、文档、视频；音频放在结果之后 |

- **错误。** Provider 或模型不支持的内容会在请求前以永久错误失败，例如
  Anthropic 上的音频，或 Chat Completions 上的文档 URL。
- **输出。** Gemini 图片和语音模型返回图片和音频 part，Chat Completions
  音频模型返回音频 part，转写文本在 `Text` 中，Bedrock 的图片块返回图片 part。

### Embedder

`openai.NewEmbedder`（`/embeddings`，也适用于 `openai.Ollama()` 等兼容服务）和
`gemini.NewEmbedder`（`batchEmbedContents`）为 agentx 的 `memory` 和 `rag` 包
实现 `memory.Embedder`。它们接受 `New` 的选项：`WithModel` 选择向量模型，
`WithDimensions` 和 `WithBatchSize` 可选。Gemini 可以为查询和存储的条目使用
不同的任务类型。provider v1.2.0 还支持 Vertex AI predict 和 Bedrock Titan Embedding；
具体选项及限制见 [Provider README](provider/README.md)。`retry.Embedder` 像重试模型调用
一样重试它们：

```go
docs, _ := gemini.NewEmbedder(gemini.WithTaskType(gemini.TaskRetrievalDocument))
queries, _ := gemini.NewEmbedder(gemini.WithTaskType(gemini.TaskRetrievalQuery))
policy := retry.Policy{}
mem := memory.New(store,
	memory.WithEmbedder(retry.Embedder(policy, docs)),
	memory.WithQueryEmbedder(retry.Embedder(policy, queries)))
```

## 存储

[pgvector 模块](pgvector/README.md) 提供数据库端余弦精确检索，覆盖 RAG
向量存储及长期记忆检索。每个命名空间隔离记录，按来源原子替换；不会自动安装 PostgreSQL
扩展或创建 ANN 索引。它不依赖 `sqlstore`，应用自行组合所需存储。

文档导入可组合 [PDF/DOCX 解析器](document/README.md) 与 [S3 Loader](s3/README.md)。
PDF 通过应用安装的 `pdftotext` 提取文字；全无文本时明确失败，只有显式配置的 OCR
Parser 才会接管。DOCX 不伪造渲染页码。专用重排见 [Provider 模块](provider/README.md)。

### 文件存储

`filestore` 把状态保存在 JSON 文件中，通过临时文件重命名替换原文件。
Unix 上的替换是原子的，其他平台遵循 `os.Rename` 的保证。新建文件的权限为
0600，新建目录为 0700，具体效果受平台权限模型影响；已有目录保持原权限。
请使用本地文件系统上的可信路径。各类存储的并发边界不同：

- Session 的修订检查只在一个存储实例内串行执行。复用同一个实例，并在外部协调
  并发删除。不同实例或进程之间需要外部协调，或改用 `sqlstore`，以避免覆盖更新；
  文件级锁不会让修订检查成为事务。
- Session 和检查点的读、替换及删除使用持久化的 `.lock` 锁文件。
  任何存储仍在使用时都不要删除这些文件。
- Memory 按下文所述协调跨进程操作。
- Vector 保留实例内快照，MCP Token 的更新只在单实例内串行执行。
  每个路径应由一个存储实例管理，避免独立写入者共享。

| 构造函数 | 实现 |
| --- | --- |
| `filestore.Sessions(dir)` | `session.Store`；`Save` 在上述协调范围内检测冲突 |
| `filestore.Checkpoints(dir)` | `loop.Checkpointer` |
| `filestore.WorkflowCheckpoints(dir)` | `workflow.Checkpointer`；`IDs` 列出已保存的运行 |
| `filestore.Memory(path)` | `memory.Store` 和 `memory.ScopedDeleter`，保存在一个文件中 |
| `filestore.Vectors(dir)` | `rag.VectorStore` 和 `rag.Revisioner`，每个来源一个文件，在内存中检索 |
| `filestore.MCPTokens(path)` | 保存 MCP OAuth 令牌和客户端注册信息的 `mcp.TokenStore` |

Memory 的每次操作都会在进程间共享的 OS 锁内重新读取文件，删除时的作用域检查也
在锁内进行。使用期间必须保留 `path + ".lock"` 锁文件，所有写入者都应使用
`filestore.Memory`，并采用支持文件锁和原子替换的本地文件系统。
锁支持 Linux、macOS、BSD、illumos 和 Windows；其他平台的 Memory 操作会返回
错误。读取失败时会返回错误，不会使用旧的缓存快照；等待锁时会响应 context 取消。

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

### SQL 存储

sqlstore v1.1.0 起的 `sqlstore.MCPTokens(db, namespace, aead, opts...)` 还可保存 MCP OAuth
Token 和客户端注册信息。必须传入用户/租户命名空间及认证加密实现；数据库只保存密文。
它不会自动协调多个实例刷新同一个 OAuth Token。装配和密钥管理见
[SQL 存储 README](sqlstore/README.md)。

`sqlstore` 通过 `database/sql` 把会话、运行检查点、Workflow 检查点和记忆
保存在 SQLite、PostgreSQL 或 MySQL（以及 MariaDB 等兼容服务）中。它不引入
任何驱动：用你自己的驱动打开数据库，再传入句柄，可以是 `*sql.DB`、`*sql.Conn`
或 `*sql.Tx`。

```go
db, _ := sql.Open("pgx", os.Getenv("DATABASE_URL")) // import _ "github.com/jackc/pgx/v5/stdlib"
opts := []sqlstore.Option{sqlstore.WithDialect(sqlstore.PostgreSQL)}
if err := sqlstore.Migrate(ctx, db, opts...); err != nil { // 幂等
	return err
}
mem := memory.New(sqlstore.Memory(db, opts...))
a := agent.MustNew("assistant", agent.WithModel(m), agent.WithMemory(mem, "user-42", 3),
	agent.WithCheckpointer(sqlstore.Checkpoints(db, opts...)))
runner := agent.NewRunner(a, sqlstore.Sessions(db, opts...))
flow := g.MustCompile( // g 是一个 *workflow.Graph
	workflow.WithCheckpointer(sqlstore.WorkflowCheckpoints(db, opts...)))
```

- **表**：每种存储一张表，除非设置 `sqlstore.WithPrefix`，否则前缀为
  `agentx_`：`sessions`、`checkpoints`、`workflow_checkpoints` 和
  `memories`。值以 JSON 文档保存。键按字节精确比较（MySQL 上为
  `VARBINARY`），时间戳为定长 UTC 字符串，向量为小端 float32 二进制。
- **冲突**：`Save` 只在会话仍是加载时的修订版本时才更新它，跨进程同样
  有效，否则返回 `session.ErrConflict`。
- **事务**：存储不会自动开启事务。单条修改语句是原子的，但 `SessionStore.Save`
  等操作可能执行多条语句。需要这些语句或多次存储操作共享事务时，传入 `*sql.Tx`；
  提交、回滚和数据库句柄的生命周期由调用方管理。
- **SQLite**：请设置 busy timeout，例如 `modernc.org/sqlite` 的
  `_pragma=busy_timeout(5000)` DSN 参数，让并发写入者等待。

两个包都通过了 agentx 的一致性测试套件：`session/sessiontest`、
`loop/checkpointtest`、`workflow/checkpointtest`、`memory/memorytest`，以及
针对 `filestore.Vectors` 的 `rag/ragtest`。

## 中间件

`logging` 用 `log/slog` 记录每次模型调用和工具调用，带有 run ID、会话 ID、
Agent 名称和轮次。`retry` 用带抖动的指数退避重试失败的模型调用。把链路追踪和
指标注册在重试之前，这样模型调用的 Span 和耗时会覆盖它的重试，每次重试记录为
一个 `agentx.retry` Span 事件：

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

`retry.Policy` 默认最多尝试 3 次，退避从 500 ms 开始、最长 30 s，倍数为 2，
抖动为 20%，并且只重试临时性故障：

- **会重试：** 可重试的 API 错误（408、409、额度未耗尽时的 429，以及 5xx）
  和传输层故障：连接被拒绝、重置或断开，HTTP/2 流被重置，HTTP 客户端超时，
  以及读取响应时连接中断。
- **不会重试：** 无效请求、无法解码的响应、证书和 TLS 错误、未知主机，以及
  调用方 context 已取消或超时。
- **`Retry-After`：** 遵循服务端给出的等待时间；超过 `MaxBackoff` 时不再
  重试。
- **流式调用**只在产出任何事件之前失败时才会重试。

`Policy.Retryable` 可以替换重试判断，默认为 `model.IsRetryable`。不经过 Agent
Loop 的模型调用，例如 `actx.Summarize` 策略使用的模型，可以用装饰器包装：
`middleware.Wrap(m, logging.Model(log), retry.Model(policy))`。
`logging.Tools(log)` 是记录工具调用的 `tool.Middleware`。

## OpenTelemetry

`otel` 把 agentx 的链路追踪和指标（遵循
[OpenTelemetry GenAI 语义约定](https://github.com/open-telemetry/semantic-conventions-genai)）
适配到 OpenTelemetry Provider。`otel.Tracer` 和 `otel.Meter` 返回
`telemetry.Tracer` 和 `telemetry.Meter`，`otel.Middleware` 返回基于它们的
`middleware.Tracing` 和 `middleware.Metrics`。传入 nil 时使用全局 Provider。

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

- **事件**：加上 `otel.WithLoggerProvider` 后，模型调用还会以日志记录的形式
  发出 `gen_ai.client.inference.operation.details` 和
  `gen_ai.client.operation.exception` 事件。
- **内容**：系统提示词、消息以及工具的参数和结果可能包含敏感数据，只有加上
  `otel.RecordContent()` 才会记录。
- **选项**：`otel.WithTraceOptions` 和 `otel.WithMetricsOptions` 传入 agentx
  `middleware` 包的选项，例如 `middleware.LegacyMetrics()`。
- **Scope**：instrumentation scope 是 `otel.ScopeName`，即
  `github.com/nostosx/agentx-contrib/otel`，版本为 `agentx.Version`。

## 示例

可运行的程序位于独立项目
[agentx-examples](https://github.com/nostosx/agentx-examples)，由它组合 core、
provider 和文件存储模块。目录布局、Provider 环境变量及 `cmd/` 下的运行命令见该项目 README。

## 开发

使用 Go 1.27.3、Task v3.53.1 和 Bash。仓库根目录不是模块，也没有提交 `go.work`。
独立检出本仓库即可，已发布的 agentx 和 kitx 版本均由 Go 模块代理下载，无需同级源码。
所有 Task 入口设置 `GOWORK=off`。

```sh
task check        # 格式、现代化、tidy 检查、vet、构建、随机顺序 race 测试
task lint         # golangci-lint，包含 integration 构建
task vulncheck    # 可达漏洞扫描，包含 integration 构建
task test         # 模块测试及工具回归测试
task test RACE=true  # 开启 race 检测
task test COVER=true  # race 测试并生成各模块 coverage.out
task test INTEGRATION=true MODULES=sqlstore  # PostgreSQL 和 MySQL DSN 必须同时配置
task check MODULES=provider  # 只检查指定模块；多个名称用引号包裹并以空格分隔
```

`task --list` 列出全部入口。`check` 只检查，不应用格式、现代化或 tidy 修改；
需要修改时显式运行 `task fmt`、`task fix` 或 `task tidy`，然后审阅差异。
现代化、vet、构建、lint 和漏洞扫描都覆盖默认及 integration 构建。
CI 按模块在 Linux、macOS 和 Windows 上运行 `task check`，lint、漏洞扫描和
双数据库集成测试独立运行。

`sqlstore` 的普通测试使用 SQLite，不需要外部服务。带 `integration` 标签运行时，
还会访问配置了 DSN 的 PostgreSQL 和 MySQL；直接运行 Go 测试时未配置的后端会
被跳过。`task test INTEGRATION=true MODULES=sqlstore` 则要求同时提供两个 DSN，并拒绝跳过或缺失一致性测试的
结果。请使用独立的临时数据库，测试会创建和删除表：

```sh
export AGENTX_IT_POSTGRES_DSN='postgres://agentx:agentx@localhost:5432/agentx_test'
export AGENTX_IT_MYSQL_DSN='agentx:agentx@tcp(localhost:3306)/agentx_test'
task test INTEGRATION=true MODULES=sqlstore
```

工具安装和贡献要求见 [CONTRIBUTING.md](CONTRIBUTING.md)；漏洞报告见
[SECURITY.md](SECURITY.md)；独立版本、core 依赖切换和发布门禁见
[RELEASING.md](RELEASING.md)。

## 许可证

[MIT](LICENSE)
