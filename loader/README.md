# Scoped cloud document connectors

Install `github.com/nostosx/agentx-contrib/loader@v1.0.2`. `NewGitHub`,
`NewNotion` and `NewFeishu` return a `Connector` implementing core `rag.Loader`
and `rag.ChangeLoader`. These connectors perform read-only API calls; they do
not create credentials, scan entire tenants, follow external links or write
provider documents.

```go
source, err := loader.NewGitHub(loader.GitHubOptions{
    Options: loader.Options{Token: tokenSource},
    Owner: "nostosx", Repo: "agentx", Ref: "main", Paths: []string{"rag"},
})
if err != nil { return err }
defer source.Close()
docs, err := source.Load(ctx)
if err != nil { return err }
_, err = index.Sync(ctx, docs) // only a complete, authoritative snapshot
```

`tokenSource` is `func(context.Context) (string, error)`, owned by the application
and usable with its Servex OAuth facilities. The HTTP implementation reuses
Servex's typed client and per-call outbound metadata, plus a restricted/bounded
transport. Every credential-bearing redirect is refused. Applications can inject
a client for private GitHub Enterprise or other trusted endpoints. That client's
network policy stays application-owned; bodies are still bounded. No automatic
retries or token acquisition occur. Failures do not expose provider raw bodies.

| Connector | Scope and content | Stable source |
| --- | --- | --- |
| GitHub | Explicit owner/repo/ref and file/directory prefixes; immutable recursive Git tree and blob API; application Parsers (defaults from core); symlinks/submodules and unsupported extensions excluded | `github://owner/repo/path` |
| Notion | Explicit PageIDs; recursive paginated block children; headings, paragraphs, lists, tasks, code, tables, quotes, toggles, columns and reference links; attachments linked only; child pages referenced, not followed | `notion://page-id-without-hyphens` |
| Feishu | Explicit docx DocumentIDs; revision-pinned, paginated blocks in hierarchy order; text, headings, lists, code, quote, todo, divider and table/grid containers | `feishu://docx/document-id` |

GitHub truncated trees, malformed or unsupported cloud block types, missing
children, pagination loops, limits, permission errors and partial listings fail
the entire Load. Notion archived/in-trash pages are explicit removals; a 403/404
is an error and never inferred deletion. Table cells retain text in hierarchy
order; this is a retrieval representation, not a lossless document renderer.
Notion defaults to API version `2025-09-03`; override APIVersion explicitly when
needed. Feishu historical-revision API reads may require edit permission even
when current-version reads are allowed. Provider errors remain a failed snapshot. Set Feishu DocumentBaseURL to the
tenant UI /docx prefix when known; no frontend URL is invented from an API host.
Notion hosted attachments use stable block references rather than expiring
download URLs, so routine URL rotation does not cause re-embedding.

For incremental indexing, inject a borrowed Servex `cache.Cache`, `encoding.Codec`
and `Scope` identifying tenant, authorization, parser and connector settings.
Use a durable/shared cache when cursors must survive restart. No separate cache
backend is implemented here. Default cursor TTL is 24 hours.

```go
changes, err := source.LoadChanges(ctx, lastCommittedCursor)
if err != nil { return err }
if _, err := index.ApplyChanges(ctx, changes); err != nil { return err }
lastCommittedCursor = changes.Cursor // persist only after the entire apply succeeds
```

Changes are computed by comparing full snapshots with cached content/metadata
hash manifests. This saves indexing/embedding work, not cloud API calls. An empty
cursor creates a baseline; expired, missing or cross-scope cursors return
`ErrCursorExpired`, requiring an explicit new baseline/reconciliation. Keep the
previous cursor after failures and retry. Never pass a delta to `Index.Sync`.
Changing selected paths/PageIDs/parser requires a new scope and a deliberate
full reconciliation of the index namespace. Do not log tokens or cursors.

协议验收使用本地 HTTP 服务覆盖三种真实 API 结构、分页、权限失败、游标隔离与大小限制。
这些测试不代表真实 GitHub/Notion/飞书账号权限验收；应用需用自己的授权范围验证云端访问。

GitHub repository names support periods, including organization profile repositories
such as `.github`; owner validation remains separate from repository validation.

Source links encode each file path segment independently, preserving slash-separated
directories and filenames containing spaces, percent signs or fragment characters.
