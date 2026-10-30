package gemini

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

// DefaultImageModel is the model selected by NewImageGenerator.
const DefaultImageModel = "gemini-nano-banana-2.1"

const maxImageResponse = 256 << 20

// ImageGenerator implements imagegen through Gemini's generateContent API,
// sharing the existing Gemini/Vertex authentication, converters and HTTP client.
// Edits use reference images and a prompt; mask/markup APIs are not supported.
// Unsupported typed options fail before a request rather than being discarded.
type ImageGenerator struct{ model *Model }

var (
	_ imagegen.Generator    = (*ImageGenerator)(nil)
	_ imagegen.Editor       = (*ImageGenerator)(nil)
	_ imagegen.Streamer     = (*ImageGenerator)(nil)
	_ imagegen.EditStreamer = (*ImageGenerator)(nil)
)

// NewImageGenerator uses the options of New and defaults to DefaultImageModel.
// It uses generateContent, including Vertex AI, and makes no API calls here.
func NewImageGenerator(opts ...Option) (*ImageGenerator, error) {
	m := newModel(opts)
	if m.model == "" {
		m.model = DefaultImageModel
	}
	if err := m.resolve(); err != nil {
		return nil, err
	}
	return &ImageGenerator{model: m}, nil
}

func (g *ImageGenerator) imageRequest(req *imagegen.Request, edit *imagegen.EditRequest) ([]byte, string, error) {
	invalid := func(message string) ([]byte, string, error) {
		return nil, "", model.Permanent(errors.New("gemini: " + message))
	}
	if req == nil || strings.TrimSpace(req.Prompt) == "" || utf8.RuneCountInString(req.Prompt) > 32000 {
		return invalid("image prompt must contain 1–32000 characters")
	}
	if req.N < 0 || req.N > 1 {
		return invalid("image generation supports one candidate per request")
	}
	if req.Quality != "" || req.OutputCompression != nil || req.Background != "" || req.SafetyIdentifier != "" {
		return invalid("quality, compression, background and safety_identifier are not supported by this image adapter")
	}
	if req.OutputFormat != "" && req.OutputFormat != "png" {
		return invalid("only default PNG output is supported")
	}
	if req.PartialImages < 0 || req.PartialImages > 3 {
		return invalid("partial image limit must be between 0 and 3")
	}
	if req.Size != "" && !slices.Contains([]string{"512", "1K", "2K", "4K"}, req.Size) {
		return invalid("image Size must be 512, 1K, 2K or 4K")
	}
	if req.AspectRatio != "" && !slices.Contains([]string{"1:1", "1:4", "1:8", "2:3", "3:2", "3:4", "4:1", "4:3", "4:5", "5:4", "8:1", "9:16", "16:9", "21:9"}, req.AspectRatio) {
		return invalid("unsupported image aspect ratio")
	}
	parts := []model.Part{model.TextPart(req.Prompt)}
	if edit != nil {
		if edit.Mask != nil || edit.Markup != nil {
			return invalid("mask and markup are not supported; describe edits in the prompt")
		}
		if len(edit.Images) < 1 || len(edit.Images) > 14 {
			return invalid("editing requires between 1 and 14 reference images, subject to model limits")
		}
		for _, p := range edit.Images {
			if p.Type != model.PartImage || len(p.Data) > 20<<20 || len(p.URL) > 28<<20 {
				return invalid("invalid reference image or input exceeds 20 MiB")
			}
		}
		parts = append(parts, edit.Images...)
	}
	name := cmp.Or(req.Model, g.model.model)
	c := &converter{gemini3: !usesBudget(name), vertex: g.model.vertex}
	contents, err := c.messages([]*model.Message{model.NewMessage(model.RoleUser, parts...)})
	if err != nil {
		return nil, "", err
	}
	imageConfig := map[string]any{}
	if req.Size != "" {
		imageConfig["imageSize"] = req.Size
	}
	if req.AspectRatio != "" {
		imageConfig["aspectRatio"] = req.AspectRatio
	}
	config := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if len(imageConfig) > 0 {
		config["imageConfig"] = imageConfig
	}
	extra := maps.Clone(req.ProviderOptions[providerName])
	for _, key := range []string{"contents", "model", "stream", "candidateCount"} {
		if _, ok := extra[key]; ok {
			return invalid("provider options cannot override " + key)
		}
	}
	if raw, ok := extra["generationConfig"]; ok {
		more, ok := raw.(map[string]any)
		if !ok {
			return invalid("generationConfig must be an object")
		}
		for key, value := range more {
			if key == "imageConfig" || key == "responseModalities" || key == "candidateCount" {
				return invalid("typed image configuration cannot be overridden")
			}
			config[key] = value
		}
		delete(extra, "generationConfig")
	}
	base := map[string]any{"contents": contents, "generationConfig": config}
	b, err := utils.EncodeBody(providerName, base, extra)
	return b, name, err
}

type imageUsage struct {
	usageMetadata
	Total        int `json:"totalTokenCount"`
	InputDetails []struct {
		Modality string `json:"modality"`
		Tokens   int    `json:"tokenCount"`
	} `json:"promptTokensDetails"`
	OutputDetails []struct {
		Modality string `json:"modality"`
		Tokens   int    `json:"tokenCount"`
	} `json:"candidatesTokensDetails"`
}

type imageResponse struct {
	response
	ImageUsage *imageUsage `json:"usageMetadata"`
	Error      *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func imagePart(p part) (*imagegen.Image, error) {
	if p.InlineData == nil {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
	if err != nil {
		return nil, fmt.Errorf("gemini: decode image: %w", err)
	}
	mt := p.InlineData.MIMEType
	if len(b) == 0 || !slices.Contains([]string{"image/png", "image/jpeg", "image/webp"}, mt) || http.DetectContentType(b) != mt {
		return nil, errors.New("gemini: invalid generated image content")
	}
	return &imagegen.Image{Part: model.Part{Type: model.PartImage, MediaType: mt, Data: b, Provider: providerName, Signature: p.ThoughtSignature}}, nil
}

func (out *imageResponse) result(name, requestID string) (*imagegen.Response, error) {
	if out.Error != nil {
		return nil, &model.APIError{Provider: providerName, StatusCode: out.Error.Code, Type: out.Error.Status, Message: out.Error.Message}
	}
	if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
		return nil, errors.New("gemini: image generation was blocked")
	}
	r := &imagegen.Response{Provider: providerName, Model: cmp.Or(out.ModelVersion, name), RequestID: cmp.Or(requestID, out.ResponseID)}
	for _, candidate := range out.Candidates {
		if candidate.FinishReason != "STOP" {
			return nil, fmt.Errorf("gemini: image generation did not complete: %s", candidate.FinishReason)
		}
		if candidate.Content == nil {
			continue
		}
		for _, p := range candidate.Content.Parts {
			if p.Thought {
				continue
			}
			img, err := imagePart(p)
			if err != nil {
				return nil, err
			}
			if img != nil {
				r.Images = append(r.Images, *img)
			}
		}
	}
	if len(r.Images) == 0 {
		return nil, errors.New("gemini: response contains no completed images")
	}
	if u := out.ImageUsage; u != nil {
		r.Usage = &imagegen.Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount, TotalTokens: u.Total}
		for _, d := range u.InputDetails {
			if d.Modality == "IMAGE" {
				r.Usage.InputImageTokens += d.Tokens
			}
			if d.Modality == "TEXT" {
				r.Usage.InputTextTokens += d.Tokens
			}
		}
		for _, d := range u.OutputDetails {
			if d.Modality == "IMAGE" {
				r.Usage.OutputImageTokens += d.Tokens
			}
			if d.Modality == "TEXT" {
				r.Usage.OutputTextTokens += d.Tokens
			}
		}
	}
	return r, nil
}

// Generate returns completed images through the independent imagegen contract.
func (g *ImageGenerator) Generate(ctx context.Context, req *imagegen.Request) (*imagegen.Response, error) {
	return g.generate(ctx, req, nil)
}

// Edit generates an edited image from reference images and a prompt.
func (g *ImageGenerator) Edit(ctx context.Context, req *imagegen.EditRequest) (*imagegen.Response, error) {
	if req == nil {
		return nil, model.Permanent(errors.New("gemini: edit request is required"))
	}
	return g.generate(ctx, &req.Request, req)
}

func (g *ImageGenerator) generate(ctx context.Context, req *imagegen.Request, edit *imagegen.EditRequest) (*imagegen.Response, error) {
	if req != nil && req.PartialImages != 0 {
		return nil, model.Permanent(errors.New("gemini: previews require streaming"))
	}
	b, name, err := g.imageRequest(req, edit)
	if err != nil {
		return nil, err
	}
	resp, err := g.model.http.Post(ctx, g.model.path(name, "generateContent"), b, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageResponse {
		return nil, errors.New("gemini: image response exceeds 256 MiB")
	}
	var out imageResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("gemini: decode image response: %w", err)
	}
	return out.result(name, cmp.Or(resp.Header.Get("X-Goog-Request-ID"), resp.Header.Get("X-Request-ID")))
}

// Stream yields available thought-image previews and one completed response.
// PartialImages bounds previews; it cannot force the model to produce previews.
func (g *ImageGenerator) Stream(ctx context.Context, req *imagegen.Request) iter.Seq2[*imagegen.StreamEvent, error] {
	return g.stream(ctx, req, nil)
}

// StreamEdit streams the same edit request used by Edit.
func (g *ImageGenerator) StreamEdit(ctx context.Context, req *imagegen.EditRequest) iter.Seq2[*imagegen.StreamEvent, error] {
	if req == nil {
		return func(yield func(*imagegen.StreamEvent, error) bool) {
			yield(nil, model.Permanent(errors.New("gemini: edit request is required")))
		}
	}
	return g.stream(ctx, &req.Request, req)
}

func (g *ImageGenerator) stream(ctx context.Context, req *imagegen.Request, edit *imagegen.EditRequest) iter.Seq2[*imagegen.StreamEvent, error] {
	return func(yield func(*imagegen.StreamEvent, error) bool) {
		body, name, err := g.imageRequest(req, edit)
		if err != nil {
			yield(nil, err)
			return
		}
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		resp, err := g.model.http.PostStream(callCtx, g.model.path(name, "streamGenerateContent")+"?alt=sse", body, nil)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		out := imageResponse{}
		acc := candidate{Content: &content{}}
		previews := 0
		limited := &io.LimitedReader{R: resp.Body, N: maxImageResponse + 1}
		for sse, readErr := range utils.ReadSSELimit(limited, 96<<20) {
			if readErr != nil {
				yield(nil, readErr)
				return
			}
			var chunk imageResponse
			if err := json.Unmarshal([]byte(sse.Data), &chunk); err != nil {
				yield(nil, fmt.Errorf("gemini: decode image stream: %w", err))
				return
			}
			if chunk.Error != nil {
				_, err := chunk.result(name, "")
				yield(nil, err)
				return
			}
			if chunk.ImageUsage != nil {
				out.ImageUsage = chunk.ImageUsage
			}
			if chunk.ModelVersion != "" {
				out.ModelVersion = chunk.ModelVersion
			}
			if chunk.ResponseID != "" {
				out.ResponseID = chunk.ResponseID
			}
			if chunk.PromptFeedback != nil {
				out.PromptFeedback = chunk.PromptFeedback
			}
			if len(chunk.Candidates) > 1 {
				yield(nil, errors.New("gemini: multiple streaming candidates are unsupported"))
				return
			}
			for _, c := range chunk.Candidates {
				if c.FinishReason != "" {
					acc.FinishReason = c.FinishReason
				}
				if c.Content == nil {
					continue
				}
				for _, p := range c.Content.Parts {
					if p.Thought {
						if p.InlineData != nil && previews < req.PartialImages {
							img, err := imagePart(p)
							if err != nil {
								yield(nil, err)
								return
							}
							if !yield(&imagegen.StreamEvent{Image: img, PartialIndex: previews}, nil) {
								return
							}
							previews++
						}
						continue
					}
					acc.Content.Parts = append(acc.Content.Parts, p)
				}
			}
		}
		if err := callCtx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if limited.N == 0 {
			yield(nil, errors.New("gemini: image stream exceeds 256 MiB"))
			return
		}
		if acc.FinishReason == "" && out.PromptFeedback == nil {
			yield(nil, fmt.Errorf("gemini: image stream ended before completion: %w", io.ErrUnexpectedEOF))
			return
		}
		out.Candidates = []candidate{acc}
		result, err := out.result(name, cmp.Or(resp.Header.Get("X-Goog-Request-ID"), resp.Header.Get("X-Request-ID")))
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&imagegen.StreamEvent{Response: result}, nil)
	}
}
