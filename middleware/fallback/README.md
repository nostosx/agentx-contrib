# Model fallback

`fallback.Model` implements the existing core `model.Model` interface and
routes across explicit adapters. It adds no cache, network client or provider
SDK. Use it directly with `agent.WithModel` or as the model of other components.

```go
router, err := fallback.New([]fallback.Target{
    {Model: primary, ModelName: "primary-model"},
    {Model: backup, ModelName: "backup-model"},
}, fallback.Options{})
if err != nil { return err }
```

Targets remain application-owned. An explicit ModelName replaces the incoming
request model for that target; otherwise Request.Model is preserved. Set target
names when routing a provider-specific model ID to a different provider. Caller
requests are cloned, not mutated. Target adapters must honor the model.Model
contract and be concurrency-safe.

The default advances only on `model.IsRetryable` errors. Authentication,
invalid input and exhausted quota therefore do not silently send the prompt
to another provider; `ShouldFallback` can deliberately choose a different policy.
Caller cancellation always stops routing. A target is attempted once per call;
compose `retry.Model` on individual targets when retrying the same backend is
wanted. Every attempted error remains in the final error chain.
The final target determines retry classification, so an earlier transient
failure does not turn a later permanent failure into a retryable error.

Streaming can switch targets only before the first event is exposed. After
text, reasoning or a tool-call delta, errors are returned without switching,
so different model/tool trajectories are not spliced together. An empty or
unfinished stream is an error. Stopping iteration stops target consumption.
The middleware records an `agentx.fallback` event when switching targets;
wrap individual targets with tracing when per-provider spans are required.
