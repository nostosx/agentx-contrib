package acceptance

import (
	"errors"

	"github.com/nostosx/agentx-contrib/provider/anthropic"
	"github.com/nostosx/agentx-contrib/provider/bedrock"
	"github.com/nostosx/agentx-contrib/provider/gemini"
	"github.com/nostosx/agentx-contrib/provider/openai"
)

// Providers constructs adapters using their documented credential environment.
// The runner invokes it only after the explicit live gate passes.
func Providers(c Config) (Bundle, error) {
	var b Bundle
	var err error
	switch c.Provider {
	case "openai", "openai-chat":
		opts := []openai.Option{openai.WithModel(c.Model)}
		if c.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(c.BaseURL))
		}
		if c.Provider == "openai-chat" {
			opts = append(opts, openai.WithChatCompletions())
		}
		b.Model, err = openai.New(opts...)
		if err == nil && c.EmbeddingModel != "" {
			b.Embedder, err = openai.NewEmbedder(append(opts, openai.WithModel(c.EmbeddingModel))...)
		}
	case "anthropic":
		opts := []anthropic.Option{anthropic.WithModel(c.Model)}
		if c.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(c.BaseURL))
		}
		b.Model, err = anthropic.New(opts...)
	case "gemini":
		opts := []gemini.Option{gemini.WithModel(c.Model)}
		if c.BaseURL != "" {
			opts = append(opts, gemini.WithBaseURL(c.BaseURL))
		}
		b.Model, err = gemini.New(opts...)
		if err == nil && c.EmbeddingModel != "" {
			b.Embedder, err = gemini.NewEmbedder(append(opts, gemini.WithModel(c.EmbeddingModel))...)
		}
	case "bedrock":
		opts := []bedrock.Option{bedrock.WithModel(c.Model)}
		if c.BaseURL != "" {
			opts = append(opts, bedrock.WithBaseURL(c.BaseURL))
		}
		b.Model, err = bedrock.New(opts...)
		if err == nil && c.EmbeddingModel != "" {
			b.Embedder, err = bedrock.NewEmbedder(append(opts, bedrock.WithModel(c.EmbeddingModel))...)
		}
	default:
		err = errors.New("unsupported provider")
	}
	return b, err
}
