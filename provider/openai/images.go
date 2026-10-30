package openai

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nostosx/kitx/dataurl"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

// DefaultImageModel is the default image generation and editing model.
const DefaultImageModel = "gpt-image-2.5-sunburst"

// maxImageResponse bounds the base64 images and metadata of a response.
const maxImageResponse = 256 << 20

// ImageGenerator implements the independent Images API, not model.Model.
// It supports generation, editing with image/mask/markup references, and
// streaming previews. GPT Image 2.5 Flare and Sunburst, including snapshots,
// are selected with WithModel or Request.Model. No model allowlist is imposed.
//
// It shares New's authentication, HTTP client, endpoint and header options.
// Compatible services must implement the JSON Images protocol; edits convert
// inline bytes to data URLs and do not upload files or fetch input/output URLs.
// Responses are bounded to 256 MiB and streaming events to 96 MiB.
// There are no automatic retries. Set a context deadline or HTTP client timeout;
// retrying a request whose outcome is unknown can generate and bill twice.
type ImageGenerator struct {
	http  utils.HTTPClient
	model string
}

var (
	_ imagegen.Generator    = (*ImageGenerator)(nil)
	_ imagegen.Editor       = (*ImageGenerator)(nil)
	_ imagegen.Streamer     = (*ImageGenerator)(nil)
	_ imagegen.EditStreamer = (*ImageGenerator)(nil)
)

// NewImageGenerator creates an Images API client. Model defaults to
// DefaultImageModel. Presets require an explicit WithModel rather than using
// their chat model. ProviderOptions["openai"] accepts extra fields such as
// moderation and input_fidelity; typed request fields cannot be overridden.
func NewImageGenerator(opts ...Option) (*ImageGenerator, error) {
	m := newModel(opts)
	if err := m.resolve(DefaultImageModel, func(*preset) string { return "" }); err != nil {
		return nil, err
	}
	return &ImageGenerator{http: m.http, model: m.model}, nil
}

type imageReference struct {
	URL    string `json:"image_url,omitempty"`
	FileID string `json:"file_id,omitempty"`
}

type imageRequest struct {
	Model             string           `json:"model"`
	Prompt            string           `json:"prompt"`
	N                 int              `json:"n,omitempty"`
	Size              string           `json:"size,omitempty"`
	Quality           string           `json:"quality,omitempty"`
	OutputFormat      string           `json:"output_format,omitempty"`
	Background        string           `json:"background,omitempty"`
	OutputCompression *int             `json:"output_compression,omitempty"`
	SafetyIdentifier  string           `json:"safety_identifier,omitempty"`
	PartialImages     int              `json:"partial_images,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
	Images            []imageReference `json:"images,omitempty"`
	Mask              *imageReference  `json:"mask,omitempty"`
	Markup            *imageReference  `json:"markup,omitempty"`
}

func (g *ImageGenerator) buildImage(req *imagegen.Request, edit *imagegen.EditRequest, stream bool) (*imageRequest, []byte, error) {
	invalid := func(message string) (*imageRequest, []byte, error) {
		return nil, nil, model.Permanent(errors.New("openai: " + message))
	}
	if req == nil || strings.TrimSpace(req.Prompt) == "" {
		return invalid("image prompt is required")
	}
	if utf8.RuneCountInString(req.Prompt) > 32000 {
		return invalid("image prompt exceeds 32000 characters")
	}
	if req.AspectRatio != "" {
		return invalid("OpenAI images use Size rather than AspectRatio")
	}
	if req.N < 0 || req.N > 10 {
		return invalid("image count must be between 1 and 10, or zero for the default")
	}
	if stream && req.N > 1 {
		return invalid("image streaming supports one output image per request")
	}
	if req.PartialImages < 0 || req.PartialImages > 3 || !stream && req.PartialImages != 0 {
		return invalid("partial_images must be 0–3 and requires streaming")
	}
	if p := req.OutputCompression; p != nil && (*p < 0 || *p > 100) {
		return invalid("output_compression must be between 0 and 100")
	}
	if utf8.RuneCountInString(req.SafetyIdentifier) > 128 {
		return invalid("safety_identifier exceeds 128 characters")
	}
	for _, field := range []struct{ name, value, allowed string }{
		{"output_format", req.OutputFormat, "png jpeg webp"},
		{"background", req.Background, "auto opaque transparent"},
		{"quality", req.Quality, "auto low medium high xhigh max"},
	} {
		if field.value != "" && !slices.Contains(strings.Fields(field.allowed), field.value) {
			return invalid("unsupported image " + field.name)
		}
	}
	if req.Background == "transparent" && req.OutputFormat == "jpeg" {
		return invalid("transparent backgrounds require png or webp output")
	}
	if req.OutputCompression != nil && req.OutputFormat != "jpeg" && req.OutputFormat != "webp" {
		return invalid("output_compression requires jpeg or webp output")
	}
	r := &imageRequest{Model: cmp.Or(req.Model, g.model), Prompt: req.Prompt,
		N: req.N, Size: req.Size, Quality: req.Quality, OutputFormat: req.OutputFormat,
		Background: req.Background, OutputCompression: req.OutputCompression,
		SafetyIdentifier: req.SafetyIdentifier, PartialImages: req.PartialImages, Stream: stream}
	if edit != nil {
		if len(edit.Images) < 1 || len(edit.Images) > 16 {
			return invalid("image editing requires between 1 and 16 input images")
		}
		if edit.Mask != nil && edit.Markup != nil {
			return invalid("mask and markup cannot be combined")
		}
		for _, p := range edit.Images {
			ref, err := imageRef(p, false)
			if err != nil {
				return nil, nil, err
			}
			r.Images = append(r.Images, *ref)
		}
		for _, field := range []struct {
			part   *model.Part
			target **imageReference
		}{{edit.Mask, &r.Mask}, {edit.Markup, &r.Markup}} {
			if field.part != nil {
				ref, err := imageRef(*field.part, true)
				if err != nil {
					return nil, nil, err
				}
				*field.target = ref
			}
		}
	}
	extra := req.ProviderOptions[providerName]
	for _, name := range []string{"model", "prompt", "n", "size", "quality", "output_format", "background", "output_compression", "safety_identifier", "partial_images", "stream", "images", "image", "mask", "markup", "provider_options", "response_format", "user", "style"} {
		if _, ok := extra[name]; ok {
			return invalid("image provider options cannot override " + name)
		}
	}
	b, err := utils.EncodeBody(providerName, r, extra)
	return r, b, err
}

func imageRef(p model.Part, pngOnly bool) (*imageReference, error) {
	if len(p.Data) > 15<<20 || len(p.URL) > 20<<20 {
		return nil, model.Permanent(errors.New("openai: inline edit image exceeds 20 MiB data URL limit"))
	}
	if p.Type != model.PartImage {
		return nil, model.Permanent(errors.New("openai: image editing requires image parts"))
	}
	if err := utils.CheckMedia(providerName, p); err != nil {
		return nil, err
	}
	sources := 0
	for _, present := range []bool{len(p.Data) > 0, p.URL != "", p.FileID != ""} {
		if present {
			sources++
		}
	}
	if sources != 1 {
		return nil, model.Permanent(errors.New("openai: image part needs exactly one of data, URL or file ID"))
	}
	if p.FileID != "" {
		return &imageReference{FileID: p.FileID}, nil
	}
	if mt, data, ok := utils.InlineData(p); ok {
		if mt != "image/png" && (pngOnly || mt != "image/jpeg" && mt != "image/webp") {
			return nil, model.Permanent(errors.New("openai: unsupported edit image media type"))
		}
		u, err := dataurl.Encode(mt, data)
		if err != nil {
			return nil, model.Permanent(fmt.Errorf("openai: encode edit image: %w", err))
		}
		if len(u) > 20<<20 {
			return nil, model.Permanent(errors.New("openai: inline edit image exceeds 20 MiB data URL limit"))
		}
		return &imageReference{URL: u}, nil
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
		return nil, model.Permanent(errors.New("openai: edit image URL needs an http or https origin without credentials"))
	}
	return &imageReference{URL: p.URL}, nil
}

// Generate calls POST /images/generations and decodes complete image bytes.
func (g *ImageGenerator) Generate(ctx context.Context, req *imagegen.Request) (*imagegen.Response, error) {
	r, b, err := g.buildImage(req, nil, false)
	if err != nil {
		return nil, err
	}
	return g.generateImage(ctx, "/images/generations", r, b)
}

// Edit calls POST /images/edits using JSON image references, including data URLs.
func (g *ImageGenerator) Edit(ctx context.Context, req *imagegen.EditRequest) (*imagegen.Response, error) {
	if req == nil {
		return nil, model.Permanent(errors.New("openai: image edit request is required"))
	}
	r, b, err := g.buildImage(&req.Request, req, false)
	if err != nil {
		return nil, err
	}
	return g.generateImage(ctx, "/images/edits", r, b)
}

func (g *ImageGenerator) generateImage(ctx context.Context, path string, req *imageRequest, body []byte) (*imagegen.Response, error) {
	resp, err := g.http.Post(ctx, path, body, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageResponse+1))
	if err != nil {
		return nil, fmt.Errorf("openai: read image response: %w", err)
	}
	if len(b) > maxImageResponse {
		return nil, errors.New("openai: image response exceeds 256 MiB")
	}
	var out imageResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("openai: decode image response: %w", err)
	}
	return out.response(req, resp.Header.Get("X-Request-ID"))
}

type imageData struct {
	Base64        string `json:"b64_json"`
	URL           string `json:"url"`
	RevisedPrompt string `json:"revised_prompt"`
}

type imageUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetails struct {
		Text  int `json:"text_tokens"`
		Image int `json:"image_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		Text  int `json:"text_tokens"`
		Image int `json:"image_tokens"`
	} `json:"output_tokens_details"`
}

type imageResponse struct {
	Created      int64           `json:"created"`
	Model        string          `json:"model"`
	Data         []imageData     `json:"data"`
	Size         string          `json:"size"`
	Quality      string          `json:"quality"`
	Background   string          `json:"background"`
	OutputFormat string          `json:"output_format"`
	Usage        *imageUsage     `json:"usage"`
	Error        *responsesError `json:"error"`
}

func (out *imageResponse) response(req *imageRequest, requestID string) (*imagegen.Response, error) {
	if out.Error != nil {
		return nil, out.Error.apiError()
	}
	if len(out.Data) != cmp.Or(req.N, 1) {
		return nil, fmt.Errorf("openai: got %d images for %d requested", len(out.Data), cmp.Or(req.N, 1))
	}
	r := &imagegen.Response{Provider: providerName, Model: cmp.Or(out.Model, req.Model), RequestID: requestID,
		Size: out.Size, Quality: out.Quality, Background: out.Background, OutputFormat: cmp.Or(out.OutputFormat, req.OutputFormat, "png")}
	if out.Created != 0 {
		r.Created = time.Unix(out.Created, 0).UTC()
	}
	if u := out.Usage; u != nil {
		r.Usage = &imagegen.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens,
			InputTextTokens: u.InputDetails.Text, InputImageTokens: u.InputDetails.Image,
			OutputTextTokens: u.OutputDetails.Text, OutputImageTokens: u.OutputDetails.Image}
	}
	for _, d := range out.Data {
		img, err := d.image(r.OutputFormat)
		if err != nil {
			return nil, err
		}
		r.Images = append(r.Images, *img)
	}
	return r, nil
}

func (d imageData) image(format string) (*imagegen.Image, error) {
	p := model.Part{Type: model.PartImage, MediaType: "image/" + format}
	switch {
	case d.Base64 != "":
		b, err := base64.StdEncoding.DecodeString(d.Base64)
		if err != nil {
			return nil, fmt.Errorf("openai: decode generated image: %w", err)
		}
		p.Data = b
		if len(b) == 0 {
			return nil, errors.New("openai: generated image is empty")
		}
		if mt := http.DetectContentType(b); mt != p.MediaType {
			return nil, fmt.Errorf("openai: generated image content type %s does not match %s", mt, p.MediaType)
		}
	case d.URL != "":
		u, err := url.Parse(d.URL)
		if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil {
			return nil, errors.New("openai: invalid generated image URL")
		}
		p.URL = d.URL
	default:
		return nil, errors.New("openai: generated image has no content")
	}
	return &imagegen.Image{Part: p, RevisedPrompt: d.RevisedPrompt}, nil
}
