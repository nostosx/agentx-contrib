# Chromium browser

This independent module implements `agentx/browser.Browser` and `Session` through
chromedp. It starts no browser at construction and downloads no executable.
Install `github.com/nostosx/agentx-contrib/browser@v1.0.0` and provide a Chromium
binary in the application environment.

```go
driver, err := browser.New(browser.Options{Binary: chromePath, Extractor: extractor})
if err != nil { return err }
session, err := driver.NewSession(ctx)
if err != nil { return err }
defer session.Close()
result, err := session.Do(ctx, corebrowser.Action{
    Operation: corebrowser.Navigate, URL: "https://example.com",
})
```

Imports are `github.com/nostosx/agentx-contrib/browser` and
`github.com/nostosx/agentx/browser` (alias `corebrowser`). `extractor` implements
core `web.Extractor`: an application can inject the independent web module's
Readability/Markdown extractor, without either contrib module importing the other.

Operations are navigate, read, click, fill, screenshot and download. Snapshots
include rendered page content plus up to 100 CSS-addressable controls; input
values are not exposed in the controls list. Screenshots return PNG bytes.
Downloads use an isolated temporary directory, generated safe names and size
limits, and return bytes before deleting the temporary file. Automatic downloads
are denied outside an explicit download operation. An interrupted download or
timed-out/cancelled in-flight operation closes the session. A private browser
process/profile belongs to each session and is joined/cleaned up by Close.

Register core `browser.Tool(session, maxBytes)` as an Agent tool. It is bound to
an application-owned session and is not marked read-only or concurrent. The
model cannot select credentials, profiles or local output paths. Image bytes
become image parts; download bytes/name/media type are in result Metadata so the
application can persist them through the existing `artifact.Store` contract.

`CheckURL` applies to explicit navigation only. Page scripts, redirects and
subresources require application/container egress isolation; this driver does
not claim to be a network sandbox. `NoSandbox` is explicit and defaults false.
Configure approval policy for interaction using the existing Agent tool options.

真实浏览器验收：设置 `AGENTX_IT_BROWSER_BINARY` 为 Chromium 绝对路径后，从仓库根目录运行
`task test INTEGRATION=true MODULES=browser`。测试动态 DOM、填写、点击、截图、下载和会话关闭；
发布门禁拒绝跳过。普通测试不启动用户浏览器，也不接入用户的登录会话。
