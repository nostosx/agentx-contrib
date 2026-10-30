package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"iter"
	"strings"
	"testing"

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

type imageFixture struct {
	response *imagegen.Response
	complete bool
	fail     error
	calls    []string
	check    func(*imagegen.Request)
}

func (g *imageFixture) Generate(_ context.Context, req *imagegen.Request) (*imagegen.Response, error) {
	g.calls = append(g.calls, "generate")
	if g.check != nil {
		g.check(req)
	}
	return g.response, g.fail
}

func (g *imageFixture) Edit(_ context.Context, req *imagegen.EditRequest) (*imagegen.Response, error) {
	g.calls = append(g.calls, "edit")
	if g.check != nil {
		g.check(&req.Request)
	}
	return g.response, g.fail
}

func (g *imageFixture) Stream(_ context.Context, req *imagegen.Request) iter.Seq2[*imagegen.StreamEvent, error] {
	return func(yield func(*imagegen.StreamEvent, error) bool) {
		g.calls = append(g.calls, "stream")
		if g.check != nil {
			g.check(req)
		}
		if g.fail != nil {
			yield(nil, g.fail)
			return
		}
		if g.complete {
			yield(&imagegen.StreamEvent{Response: g.response}, nil)
		}
	}
}

func validImageResponse(t *testing.T) *imagegen.Response {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return &imagegen.Response{RequestID: "image-request", Images: []imagegen.Image{{Part: model.ImagePart("image/png", b.Bytes())}}, Usage: &imagegen.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}}
}

func TestImageAcceptanceGate(t *testing.T) {
	t.Parallel()
	for _, c := range []Config{
		{Provider: "openai-images", Model: "gpt-image-2.5-flare"},
		{Live: true, Provider: "openai-images", Checks: []string{"generate"}},
		{Live: true, Provider: "openai-images", Model: "image", Checks: []string{"tools"}},
		{Live: true, Provider: "openai-images", Model: "image", Checks: []string{"edit"}},
		{Live: true, Provider: "openai", Model: "chat", Checks: []string{"edit"}},
	} {
		r := Run(t.Context(), c, func(Config) (Bundle, error) { t.Fatal("factory called before gate"); return Bundle{}, nil })
		if r.Status != "unverified" {
			t.Fatalf("report = %+v", r)
		}
	}
}

func TestImageAcceptanceEvidence(t *testing.T) {
	t.Parallel()
	resp := validImageResponse(t)
	g := &imageFixture{response: resp, complete: true, check: func(req *imagegen.Request) {
		if req.N != 1 || req.Size != "1024x1024" || req.Quality != "low" || req.OutputFormat != "png" || req.Model != "explicit-image" {
			t.Fatalf("unbounded image request = %+v", req)
		}
	}}
	r := Run(t.Context(), Config{Live: true, Provider: "openai-images", Model: "explicit-image", Checks: []string{"generate", "stream", "edit"}, Media: new(model.ImagePart("image/png", []byte("fixture")))}, func(Config) (Bundle, error) { return Bundle{Images: g}, nil })
	if r.Status != "passed" || len(g.calls) != 3 {
		t.Fatalf("report = %+v, calls = %v", r, g.calls)
	}
	for _, result := range r.Results {
		if result.Status == "not_requested" {
			continue
		}
		if result.Status != "passed" || len(result.Images) != 1 || result.Images[0].Width != 2 || result.Images[0].Height != 3 || len(result.Images[0].SHA256) != 64 || result.ImageUsage.TotalTokens != 5 || result.RequestID != "image-request" {
			t.Fatalf("result = %+v", result)
		}
	}
	g.calls = nil
	r = Run(t.Context(), Config{Live: true, Provider: "openai-images", Model: "explicit-image"}, func(Config) (Bundle, error) { return Bundle{Images: g}, nil })
	if r.Status != "passed" || strings.Join(g.calls, ",") != "generate,stream" {
		t.Fatalf("image defaults = %+v, %v", r, g.calls)
	}
}

func TestImageAcceptanceFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		g     *imageFixture
		check string
	}{
		{name: "no completion", g: &imageFixture{}, check: "stream"},
		{name: "empty", g: &imageFixture{response: &imagegen.Response{}}, check: "generate"},
		{name: "invalid image", g: &imageFixture{response: &imagegen.Response{Images: []imagegen.Image{{Part: model.ImagePart("image/png", []byte("secret-image-content"))}}}}, check: "generate"},
		{name: "provider error", g: &imageFixture{fail: errors.New("secret-api-key")}, check: "generate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run(t.Context(), Config{Live: true, Provider: "openai-images", Model: "image", Checks: []string{tc.check}}, func(Config) (Bundle, error) { return Bundle{Images: tc.g}, nil })
			b, err := json.Marshal(r)
			if err != nil || r.Status != "failed" || strings.Contains(string(b), "secret") {
				t.Fatalf("report = %s, %v", b, err)
			}
		})
	}
}
