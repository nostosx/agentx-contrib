# Development trace and evaluation viewer

Unreleased, explicitly mounted HTTP viewer built on `telemetry.Tracer` and
`eval.Report`. It does not start a listener, register global routes or run
model calls. Applications keep their existing Agent/evaluation execution path.

```go
collector := debug.New(debug.Options{Capacity: 256})
// Use collector with agentx middleware.Tracing, like any telemetry.Tracer.
viewer, err := debug.NewHandler(collector, debug.HTTPOptions{
    Authorize: func(r *http.Request) bool { return authorizedDeveloper(r) },
})
if err != nil { return err }
mux.Handle("/debug/", http.StripPrefix("/debug", viewer))
// report, err := dataset.Evaluate(ctx, task, ...)
// viewer.PublishReport("candidate", report)
```

Imports: `github.com/nostosx/agentx-contrib/debug`, `net/http`. Mount behind the
application's authenticated developer session (for example, existing servex
session/auth middleware). Browser requests use same-origin cookies. The viewer
does not implement a second session/auth system. Process profiling remains the
separate servex-contrib `middleware/profile` facility; no pprof implementation
is duplicated here.

The page lists completed spans, parent relationships, durations, selected
attributes, events and error types. The default collector keeps 256 completed
spans (maximum 10000), at most 64 events and 32 attributes per span. String
values are bounded to 256 bytes. Snapshots are independent copies, and span
End is idempotent. Active spans are published only when completed. This is a
per-process development buffer, not a durable or distributed trace backend;
existing contrib OTel remains the production export adapter.

Prompt, tool payload and other arbitrary attributes are omitted by default.
`Options.Attributes` explicitly opts in additional scalar attributes. Raw
error messages are never retained. Keep one collector per application access
domain: authorizing a request grants access to its whole collector.

`PublishReport` snapshots up to 32 reports, at most 10000 cases and 1 MiB each.
Case/evaluation names over 256 bytes are rejected, preserving exact identities
for comparison instead of merging names through truncation.
It discards inputs, outputs, expected values, metadata, free-form reasons,
labels and raw error messages. `/reports` exposes aggregate outcomes and
per-case summaries; `/compare?baseline=...&candidate=...` uses core eval.Compare.
The browser provides baseline/candidate selection and trace details. It does
not accept dataset paths or expose a remote execution endpoint. Replace an
existing report ID to update it; construct a new viewer to reset the report set.

All routes require an explicit Authorize callback and accept GET only. They
disable caching, set a restrictive CSP, and render values through textContent.
Mount at a path ending in slash so relative asset/API URLs resolve correctly.
Unit tests cover authentication, report privacy/copying, comparisons, bounded
retention, concurrent recording and snapshot independence.

Browser acceptance is also committed, using an isolated Go HTTP fixture,
real eval.Dataset execution, Chromium and WebKit. Install Playwright 1.64.1
and both browser engines outside this Go module, then run from the contrib root:

```sh
AGENTX_IT_NODE=/absolute/path/to/node \
AGENTX_IT_PLAYWRIGHT=/absolute/path/to/node_modules/playwright/index.mjs \
task test INTEGRATION=true MODULES=debug
```

The gate requires both engines and rejects skips. It verifies authentication,
mounted asset/API paths, trace selection, baseline comparison, refresh, private
data omission and that HTML-like trace names stay text. CI and debug releases
run the same gate. It does not need a model provider or an application server.
