# External web search

Adapters for `agentx/search.Searcher`. `NewTavily` uses basic web
search; `NewSerper` returns Google organic results. Applications supply API
keys explicitly; constructors do not read credentials from the environment.

```go
client, err := search.NewTavily(search.Options{APIKey: apiKey})
if err != nil { return err }
webSearch, err := coresearch.Tool(client, 5)
if err != nil { return err }
// agent.WithTools(webSearch)
```

Import `search` from `github.com/nostosx/agentx-contrib/search` and `coresearch`
from `github.com/nostosx/agentx/search`. Core v1.3.0 or later is required; install the independently versioned contrib module.

`Search(ctx, Request{Query: "...", Limit: 5})` preserves titles, URLs, snippets,
provider scores when available, and Tavily's request ID. Zero Limit selects 5.
Tavily supports at most 20 results; Serper at most 100. The Agent tool applies
an application-configured result cap. Neither adapter fetches result URLs,
retries a paid request, or treats returned text as trusted instructions.

Endpoint is a complete, trusted HTTP(S) search URL. An injected HTTP client
retains its transport and timeout; redirects are disabled to protect keys.
Responses are limited to 4 MiB; HTTP error bodies are not exposed. Cancellation
propagates to the request. Empty queries, invalid source URLs and malformed
JSON fail explicitly. Scores from different search engines are not comparable.

Tests use local HTTP servers, including authentication, result mapping,
redirect refusal, limits, invalid responses and cancellation. Ordinary tests
do not use service credentials or consume search credits.

API references: [Tavily Search](https://docs.tavily.com/documentation/api-reference/endpoint/search),
[Serper](https://serper.dev/).
