package openai

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"

	"github.com/nostosx/agentx-contrib/provider/internal/utils"
	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

// Stream generates a single image, yielding previews and one final response.
func (g *ImageGenerator) Stream(ctx context.Context, req *imagegen.Request) iter.Seq2[*imagegen.StreamEvent, error] {
	return g.streamImage(ctx, req, nil)
}

// StreamEdit edits images, yielding previews and one final response.
func (g *ImageGenerator) StreamEdit(ctx context.Context, req *imagegen.EditRequest) iter.Seq2[*imagegen.StreamEvent, error] {
	if req == nil {
		return func(yield func(*imagegen.StreamEvent, error) bool) {
			yield(nil, model.Permanent(errors.New("openai: image edit request is required")))
		}
	}
	return g.streamImage(ctx, &req.Request, req)
}

type imageStreamEvent struct {
	imageResponse
	Type         string    `json:"type"`
	Base64       string    `json:"b64_json"`
	PartialIndex int       `json:"partial_image_index"`
	Code         errorCode `json:"code"`
	Message      string    `json:"message"`
}

func (g *ImageGenerator) streamImage(ctx context.Context, req *imagegen.Request, edit *imagegen.EditRequest) iter.Seq2[*imagegen.StreamEvent, error] {
	return func(yield func(*imagegen.StreamEvent, error) bool) {
		r, body, err := g.buildImage(req, edit, true)
		if err != nil {
			yield(nil, err)
			return
		}
		path, kind := "/images/generations", "image_generation"
		if edit != nil {
			path, kind = "/images/edits", "image_edit"
		}
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		resp, err := g.http.PostStream(callCtx, path, body, nil)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()
		limited := &io.LimitedReader{R: resp.Body, N: maxImageResponse + 1}
		for sse, readErr := range utils.ReadSSELimit(limited, 96<<20) {
			if readErr != nil {
				yield(nil, fmt.Errorf("openai: read image stream: %w", readErr))
				return
			}
			var ev imageStreamEvent
			if err := json.Unmarshal([]byte(sse.Data), &ev); err != nil {
				yield(nil, fmt.Errorf("openai: decode image event: %w", err))
				return
			}
			if ev.Error != nil {
				yield(nil, ev.Error.apiError())
				return
			}
			typ := cmp.Or(ev.Type, sse.Event)
			switch typ {
			case "error":
				e := responsesError{Code: ev.Code, Message: ev.Message}
				yield(nil, e.apiError())
				return
			case kind + ".partial_image":
				img, err := (imageData{Base64: ev.Base64}).image(cmp.Or(ev.OutputFormat, r.OutputFormat, "png"))
				if err != nil {
					yield(nil, err)
					return
				}
				if !yield(&imagegen.StreamEvent{Image: img, PartialIndex: ev.PartialIndex}, nil) {
					return
				}
			case kind + ".completed":
				ev.Data = []imageData{{Base64: ev.Base64}}
				out, err := ev.response(r, resp.Header.Get("X-Request-ID"))
				if err != nil {
					yield(nil, err)
					return
				}
				yield(&imagegen.StreamEvent{Response: out}, nil)
				return
			default:
				if strings.HasSuffix(typ, ".failed") {
					yield(nil, errors.New("openai: image generation failed"))
					return
				}
			}
		}
		if err := callCtx.Err(); err != nil {
			yield(nil, err)
			return
		}
		if limited.N == 0 {
			yield(nil, model.Permanent(errors.New("openai: image stream exceeds 256 MiB")))
			return
		}
		yield(nil, fmt.Errorf("openai: image stream ended before completion: %w", io.ErrUnexpectedEOF))
	}
}
