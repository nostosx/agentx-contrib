package openai

import (
	"net/url"
	"strings"
)

// A preset configures a Model for an OpenAI-compatible service: its base
// URL, the environment variable holding its key, a default model and the
// request fields it does not accept. Presets use the Chat Completions API
// (the one every listed service implements) except Azure, which serves the
// Responses API. Options passed with a preset take precedence over it,
// whatever their order, so WithBaseURL points a preset at another host
// and WithModel replaces its default model.
type preset struct {
	// name is the OpenTelemetry gen_ai.provider.name reported by Describe.
	name        string
	baseURL     string
	baseURLEnv  string
	keyEnv      string
	keyOptional bool
	model       string
	embedModel  string
	chat        bool
	quirks
}

// quirks lists Chat Completions features a service lacks.
type quirks struct {
	// legacyMaxTokens sends max_tokens instead of max_completion_tokens.
	legacyMaxTokens bool
	// noStreamOptions omits stream_options; such services report usage in
	// the final chunk on their own.
	noStreamOptions bool
	// noNames omits messages[].name, which the service rejects.
	noNames bool
	// jsonObject asks for JSON with response_format json_object and puts
	// the schema in the system prompt, for services without json_schema.
	jsonObject bool
	// noToolChoice omits tool_choice; ToolChoiceNone omits the tools.
	noToolChoice bool
	// replayReasoning sends the reasoning of earlier assistant messages
	// back in reasoning_content.
	replayReasoning bool
	// dimensionsField names the embeddings dimension field.
	dimensionsField string
}

func (p *preset) title() string {
	if p.name == providerName {
		return "OpenAI"
	}
	return p.name
}

// Presets, as documented by each service in October 2026.
var (
	// https://docs.ollama.com/api/openai-compatibility: the key is ignored
	// locally and needed for https://ollama.com/v1; max_completion_tokens
	// and tool_choice are not supported. Model names from
	// https://docs.ollama.com/quickstart and
	// https://docs.ollama.com/capabilities/embeddings.
	presetOllama = preset{
		name: "ollama", baseURL: "http://localhost:11434/v1", keyEnv: "OLLAMA_API_KEY", keyOptional: true,
		model: "gemma4:e2b", embedModel: "embeddinggemma", chat: true,
		legacyMaxTokens: true, noToolChoice: true,
	}
	// https://api-docs.deepseek.com/api/create-chat-completion: max_tokens
	// only, response_format text or json_object only; thinking mode needs
	// reasoning_content sent back when tools are present
	// (https://api-docs.deepseek.com/guides/thinking_mode).
	presetDeepSeek = preset{
		name: "deepseek", baseURL: "https://api.deepseek.com", keyEnv: "DEEPSEEK_API_KEY",
		model: "deepseek-flash", chat: true,
		legacyMaxTokens: true, jsonObject: true, replayReasoning: true,
	}
	// https://openrouter.ai/docs/quickstart and
	// https://openrouter.ai/docs/guides/routing/auto-model-selection.
	presetOpenRouter = preset{
		name: "openrouter", baseURL: "https://openrouter.ai/api/v1", keyEnv: "OPENROUTER_API_KEY",
		model: "openrouter/auto", chat: true,
	}
	// https://console.groq.com/docs/openai rejects messages[].name;
	// https://console.groq.com/docs/models lists openai/gpt-oss-120b in
	// production, with strict structured outputs.
	presetGroq = preset{
		name: "groq", baseURL: "https://api.groq.com/openai/v1", keyEnv: "GROQ_API_KEY",
		model: "openai/gpt-oss-120b", chat: true,
		noNames: true,
	}
	// https://docs.x.ai/docs/models recommends grok-4.7.
	presetXAI = preset{
		name: "x_ai", baseURL: "https://api.x.ai/v1", keyEnv: "XAI_API_KEY",
		model: "grok-4.7", chat: true,
	}
	// https://docs.mistral.ai/openapi.yaml: requests reject unknown fields,
	// so max_completion_tokens, stream_options and messages[].name are not
	// sent; embeddings take output_dimension.
	presetMistral = preset{
		name: "mistral_ai", baseURL: "https://api.mistral.ai/v1", keyEnv: "MISTRAL_API_KEY",
		model: "mistral-large-latest", embedModel: "mistral-embed", chat: true,
		legacyMaxTokens: true, noStreamOptions: true, noNames: true, dimensionsField: "output_dimension",
	}
	// https://docs.together.ai/docs/openai-api-compatibility and
	// https://docs.together.ai/reference/chat-completions-1: max_tokens
	// only, no stream_options.
	presetTogether = preset{
		name: "together", baseURL: "https://api.together.ai/v1", keyEnv: "TOGETHER_API_KEY",
		model: "openai/gpt-oss-120b", chat: true,
		legacyMaxTokens: true, noStreamOptions: true,
	}
	// https://docs.fireworks.ai/api-reference/post-chatcompletions.
	presetFireworks = preset{
		name: "fireworks", baseURL: "https://api.fireworks.ai/inference/v1", keyEnv: "FIREWORKS_API_KEY",
		model: "accounts/fireworks/models/kimi-k2-instruct-0905", chat: true,
	}
	// https://docs.vllm.ai/en/latest/serving/online_serving/openai_compatible_server/:
	// the key is the server's --api-key, if any.
	presetVLLM = preset{
		name: "vllm", baseURL: "http://localhost:8000/v1", keyEnv: "VLLM_API_KEY", keyOptional: true, chat: true,
	}
	// https://lmstudio.ai/docs/developer/openai-compat.
	presetLMStudio = preset{
		name: "lmstudio", baseURL: "http://localhost:1234/v1", keyOptional: true, chat: true,
		legacyMaxTokens: true,
	}
)

func usePreset(p preset) Option {
	return func(m *Model) {
		m.preset = &p
		m.chat = m.chat || p.chat
		m.legacyMaxTokens = m.legacyMaxTokens || p.legacyMaxTokens
	}
}

// Ollama configures a local Ollama server at http://localhost:11434/v1
// with model gemma4:e2b; no key is needed. For Ollama's cloud, use
// WithBaseURL("https://ollama.com/v1") with $OLLAMA_API_KEY. Embedders
// default to embeddinggemma.
func Ollama() Option { return usePreset(presetOllama) }

// DeepSeek configures https://api.deepseek.com with $DEEPSEEK_API_KEY and
// model deepseek-flash. Structured output uses JSON mode with the schema
// in the system prompt, and reasoning is sent back as thinking mode with
// tools requires.
func DeepSeek() Option { return usePreset(presetDeepSeek) }

// OpenRouter configures https://openrouter.ai/api/v1 with
// $OPENROUTER_API_KEY and the openrouter/auto router. Its optional
// attribution headers can be added with WithHeader("HTTP-Referer", url)
// and WithHeader("X-OpenRouter-Title", name).
func OpenRouter() Option { return usePreset(presetOpenRouter) }

// Groq configures https://api.groq.com/openai/v1 with $GROQ_API_KEY and
// model openai/gpt-oss-120b. Message names are not sent.
func Groq() Option { return usePreset(presetGroq) }

// XAI configures https://api.x.ai/v1 with $XAI_API_KEY and model grok-4.7.
// xAI's reasoning models reject stop sequences.
func XAI() Option { return usePreset(presetXAI) }

// Mistral configures https://api.mistral.ai/v1 with $MISTRAL_API_KEY and
// model mistral-large-latest; embedders default to mistral-embed.
func Mistral() Option { return usePreset(presetMistral) }

// Together configures https://api.together.ai/v1 with $TOGETHER_API_KEY
// and model openai/gpt-oss-120b.
func Together() Option { return usePreset(presetTogether) }

// Fireworks configures https://api.fireworks.ai/inference/v1 with
// $FIREWORKS_API_KEY and model
// accounts/fireworks/models/kimi-k2-instruct-0905.
func Fireworks() Option { return usePreset(presetFireworks) }

// VLLM configures a vLLM server at http://localhost:8000/v1; use
// WithBaseURL for another address. WithModel is required, with the name
// the server serves. $VLLM_API_KEY is sent if set.
func VLLM() Option { return usePreset(presetVLLM) }

// LMStudio configures an LM Studio server at http://localhost:1234/v1.
// WithModel is required.
func LMStudio() Option { return usePreset(presetLMStudio) }

// WithAzure configures an Azure OpenAI resource through its v1 API
// (https://learn.microsoft.com/en-us/azure/foundry/openai/api-version-lifecycle):
// the base URL is endpoint + "/openai/v1", where endpoint is like
// https://NAME.openai.azure.com or https://NAME.services.ai.azure.com, and
// no api-version is needed. WithModel names the deployment and is
// required. The key, from WithAPIKey or $AZURE_OPENAI_API_KEY, is sent in
// the api-key header; for Microsoft Entra ID, use WithTokenSource with
// tokens for the scope https://ai.azure.com/.default instead. Requests use
// the Responses API unless WithChatCompletions is set.
func WithAzure(endpoint string) Option {
	return func(m *Model) {
		m.preset = &preset{name: "azure.ai.openai", baseURL: strings.TrimRight(endpoint, "/") + "/openai/v1", keyEnv: "AZURE_OPENAI_API_KEY"}
		m.azure = true
	}
}

// WithAzureDeployment configures a deployment of an Azure OpenAI resource
// through the dated API
// (https://learn.microsoft.com/en-us/azure/foundry/openai/reference):
// requests go to endpoint + "/openai/deployments/" + deployment with the
// api-version query parameter, such as the GA version "2024-10-21". It
// selects Chat Completions; authentication is as for WithAzure.
func WithAzureDeployment(endpoint, deployment, apiVersion string) Option {
	return func(m *Model) {
		base := strings.TrimRight(endpoint, "/") + "/openai/deployments/" + url.PathEscape(deployment)
		m.preset = &preset{name: "azure.ai.openai", baseURL: base, keyEnv: "AZURE_OPENAI_API_KEY", model: deployment, embedModel: deployment, chat: true}
		m.azure, m.chat = true, true
		m.http.Query = url.Values{"api-version": {apiVersion}}
	}
}
