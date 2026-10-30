// Package acceptance checks a provider against small, bounded live requests.
// Reports contain fixed error categories, never model output or API error bodies.
package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/memory"
	"github.com/nostosx/agentx/model"
)

// Config explicitly selects one provider, model and set of acceptance checks.
type Config struct {
	Live           bool
	Provider       string
	Model          string
	EmbeddingModel string
	BaseURL        string
	Checks         []string
	MaxTokens      int
	Timeout        time.Duration
	Media          *model.Part
}

// Bundle is constructed only after the live gate and configuration pass.
type Bundle struct {
	Model    model.Model
	Embedder memory.Embedder
	Images   imagegen.Generator
}

// Factory obtains credentials and constructs exactly the selected adapters.
type Factory func(Config) (Bundle, error)

// Result records one check without exposing request or response content.
type Result struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	ErrorCode  string          `json:"error_code,omitempty"`
	Usage      model.Usage     `json:"usage"`
	ImageUsage *imagegen.Usage `json:"image_usage,omitempty"`
	Images     []ImageEvidence `json:"images,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
}

// Report is evidence for the requested checks on this model and date only.
type Report struct {
	Time           time.Time `json:"time"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	EmbeddingModel string    `json:"embedding_model,omitempty"`
	Status         string    `json:"status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Results        []Result  `json:"results"`
}

var checkNames = []string{"generate", "stream", "tools", "structured", "embedding", "media", "edit"}

// Run never calls factory unless Live is true. Every requested check must pass;
// unrequested capabilities remain visible as not_requested in the report.
func Run(ctx context.Context, c Config, factory Factory) Report {
	r := Report{Time: time.Now().UTC(), Provider: c.Provider, Model: c.Model, EmbeddingModel: c.EmbeddingModel, Status: "unverified"}
	if len(c.Checks) == 0 {
		c.Checks = checkNames[:4]
		if imageProvider(c.Provider) {
			c.Checks = []string{"generate", "stream"}
		}
	}
	for _, name := range checkNames {
		status := "not_requested"
		if slices.Contains(c.Checks, name) {
			status = "unverified"
		}
		r.Results = append(r.Results, Result{Name: name, Status: status})
	}
	if !c.Live {
		r.ErrorCode = "live_not_enabled"
		return r
	}
	if err := validate(&c); err != nil {
		r.ErrorCode = "invalid_configuration"
		return r
	}
	b, err := factory(c)
	if err != nil {
		r.ErrorCode = "adapter_configuration_failed"
		return r
	}
	r.Status = "passed"
	for i := range r.Results {
		result := &r.Results[i]
		if result.Status == "not_requested" {
			continue
		}
		start := time.Now()
		checkCtx, cancel := context.WithTimeout(ctx, c.Timeout)
		if imageProvider(c.Provider) {
			err = checkImages(checkCtx, c, b.Images, result)
		} else {
			result.Usage, err = check(checkCtx, c, b, result.Name)
		}
		cancel()
		result.DurationMS = time.Since(start).Milliseconds()
		result.Status = "passed"
		if err != nil {
			result.Status, result.ErrorCode = "failed", errorCode(err)
			r.Status = "failed"
		}
	}
	return r
}

func validate(c *Config) error {
	if !slices.Contains([]string{"openai", "openai-chat", "openai-images", "gemini-images", "anthropic", "gemini", "bedrock"}, c.Provider) || strings.TrimSpace(c.Model) == "" {
		return errors.New("provider and explicit model required")
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 512
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxTokens < 64 || c.MaxTokens > 4096 || c.Timeout <= 0 || c.Timeout > 5*time.Minute {
		return errors.New("invalid request budget")
	}
	seen := map[string]bool{}
	for _, name := range c.Checks {
		if !slices.Contains(checkNames, name) || seen[name] {
			return errors.New("unknown or repeated check")
		}
		seen[name] = true
		if imageProvider(c.Provider) {
			if name != "generate" && name != "stream" && name != "edit" {
				return errors.New("unsupported images check")
			}
		} else if name == "edit" {
			return errors.New("image editing requires an image provider")
		}
	}
	if seen["embedding"] && (c.EmbeddingModel == "" || c.Provider == "anthropic") {
		return errors.New("embedding requires a supported provider and explicit embedding model")
	}
	if seen["media"] && (c.Media == nil || !c.Media.IsMedia() || len(c.Media.Data) == 0 || len(c.Media.Data) > 5<<20) {
		return errors.New("media requires a bounded inline fixture")
	}
	if seen["edit"] && (c.Media == nil || c.Media.Type != model.PartImage || len(c.Media.Data) == 0 || len(c.Media.Data) > 5<<20) {
		return errors.New("image edit requires a bounded inline image fixture")
	}
	return nil
}

func request(c Config, prompt string) *model.Request {
	return &model.Request{Model: c.Model, Messages: []*model.Message{model.UserText(prompt)}, MaxTokens: c.MaxTokens}
}

func check(ctx context.Context, c Config, b Bundle, name string) (model.Usage, error) {
	if err := ctx.Err(); err != nil {
		return model.Usage{}, err
	}
	if name == "embedding" {
		return model.Usage{}, checkEmbedding(ctx, b.Embedder)
	}
	if b.Model == nil {
		return model.Usage{}, errors.New("no model")
	}
	switch name {
	case "stream":
		var response *model.Response
		var done bool
		for ev, err := range b.Model.Stream(ctx, request(c, "Reply with exactly agentx-ok.")) {
			if err != nil {
				return model.Usage{}, err
			}
			if ev == nil {
				continue
			}
			if done {
				return model.Usage{}, errors.New("event after stream completion")
			}
			if ev.Type == model.StreamDone {
				done, response = true, ev.Response
			}
		}
		return validateResponse(response, "agentx-ok")
	case "tools":
		return checkTools(ctx, c, b.Model)
	case "structured":
		req := request(c, `Return a JSON object with status equal to "agentx-ok".`)
		req.ResponseFormat = &model.ResponseFormat{Name: "acceptance", Strict: true, Schema: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["agentx-ok"]}},"required":["status"],"additionalProperties":false}`)}
		resp, err := b.Model.Generate(ctx, req)
		if err != nil {
			return model.Usage{}, err
		}
		usage, err := validateResponse(resp, "")
		if err != nil {
			return usage, err
		}
		var value map[string]string
		if json.Unmarshal([]byte(resp.Text()), &value) != nil || len(value) != 1 || value["status"] != "agentx-ok" {
			return usage, errors.New("structured response does not match schema")
		}
		return usage, nil
	case "media":
		req := request(c, "Briefly describe the attached content.")
		req.Messages[0].Parts = append(req.Messages[0].Parts, *c.Media)
		resp, err := b.Model.Generate(ctx, req)
		if err != nil {
			return model.Usage{}, err
		}
		return validateResponse(resp, "")
	default:
		resp, err := b.Model.Generate(ctx, request(c, "Reply with exactly agentx-ok."))
		if err != nil {
			return model.Usage{}, err
		}
		return validateResponse(resp, "agentx-ok")
	}
}

func validateResponse(resp *model.Response, want string) (model.Usage, error) {
	if resp == nil || resp.Message == nil || strings.TrimSpace(resp.Text()) == "" {
		return model.Usage{}, errors.New("missing response text")
	}
	if resp.FinishReason != model.FinishStop || (want != "" && strings.TrimSpace(resp.Text()) != want) {
		return resp.Usage, errors.New("unexpected response or incomplete finish")
	}
	if resp.Usage.InputTokens < 0 || resp.Usage.OutputTokens < 0 || resp.Usage.TotalTokens() <= 0 {
		return resp.Usage, errors.New("missing or invalid token usage")
	}
	return resp.Usage, nil
}

func checkTools(ctx context.Context, c Config, m model.Model) (model.Usage, error) {
	req := request(c, `Call the acceptance_echo tool once with value "agentx-ok". After receiving its result, reply with exactly its value.`)
	req.Tools = []*model.ToolSpec{{Name: "acceptance_echo", Description: "Return the supplied value", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","enum":["agentx-ok"]}},"required":["value"],"additionalProperties":false}`)}}
	resp, err := m.Generate(ctx, req)
	if err != nil {
		return model.Usage{}, err
	}
	if resp == nil || resp.Message == nil || len(resp.ToolCalls()) != 1 {
		return model.Usage{}, errors.New("expected one tool call")
	}
	call := resp.ToolCalls()[0]
	var args map[string]string
	if call.ID == "" || call.Name != "acceptance_echo" || json.Unmarshal(call.Arguments, &args) != nil || args["value"] != "agentx-ok" {
		return resp.Usage, errors.New("invalid tool call")
	}
	usage := resp.Usage
	req.Messages = append(req.Messages, resp.Message, model.ToolResults(&model.ToolResult{CallID: call.ID, Name: call.Name, Content: "agentx-ok"}))
	resp, err = m.Generate(ctx, req)
	if err != nil {
		return usage, err
	}
	finalUsage, err := validateResponse(resp, "agentx-ok")
	usage.Add(finalUsage)
	return usage, err
}

func checkEmbedding(ctx context.Context, e memory.Embedder) error {
	if e == nil {
		return errors.New("no embedder")
	}
	vecs, err := e.Embed(ctx, []string{"agentx acceptance first", "agentx acceptance second"})
	if err != nil {
		return err
	}
	if len(vecs) != 2 || len(vecs[0]) == 0 || len(vecs[0]) != len(vecs[1]) {
		return errors.New("invalid embedding shape")
	}
	for _, vec := range vecs {
		var norm float64
		for _, v := range vec {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return errors.New("nonfinite embedding")
			}
			norm += float64(v) * float64(v)
		}
		if norm == 0 {
			return errors.New("zero embedding")
		}
	}
	return nil
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	if api, ok := errors.AsType[*model.APIError](err); ok {
		return fmt.Sprintf("http_%d", api.StatusCode)
	}
	return "check_failed"
}
