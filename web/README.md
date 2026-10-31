# Web content

`web` implements the `agentx/web.Reader` and `Extractor` contracts using
Readability and HTML-to-Markdown. It requires agentx v1.4.0 and the independently
released servex-contrib HTTP foundation. Install this module alone:

```sh
go get github.com/nostosx/agentx-contrib/web@v1.0.0
```

```go
reader, err := web.NewReader(web.Options{})
if err != nil { return err }
defer reader.Close()
doc, err := reader.Read(ctx, "https://example.com/article")
```

Import `github.com/nostosx/agentx-contrib/web` as `web`. Use core
`github.com/nostosx/agentx/web.Tool(reader, 12000)` to register `web_read` with an
Agent, or core `web.Loader{Reader: reader, URLs: urls}` for a complete RAG snapshot.
Extraction retains the final source URL, title, plain text, Markdown and absolute
HTTP links. Scripts/styles are excluded. `FullPage` preserves forms/navigation
instead of article selection. Text is source evidence and may contain hostile
instructions; it is never promoted to trusted system instructions.

The default Servex HTTP client rejects private/reserved addresses, validates
redirects, pins DNS addresses and bounds bodies. It does not use environment
proxies. Injected clients own their own network/proxy policy. HTML, XHTML and
plain text are supported, with charset decoding and byte limits before and after
decoding. No linked resources are crawled. Unsupported types and oversized
results fail instead of silently truncating an indexed snapshot.

An optional borrowed `servex-contrib/cache/loader.Loader[coreweb.Document]`
coalesces/caches reads. Select the Cache and Codec in the application and set
`Namespace` to include tenant, authorization and extraction configuration.
Close that Loader in its owner; Reader never closes borrowed clients/loaders.

网页正文提取、Markdown、来源和链接均保留。默认复用 Servex 的受限 HTTP 客户端；
可注入共享 Cache Loader。应用选择身份、网络策略和缓存作用域；索引加载失败时不返回部分快照。
