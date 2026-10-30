package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/png" // Decode PNG acceptance artifacts.

	"github.com/nostosx/agentx/imagegen"
	"github.com/nostosx/agentx/model"
)

// ImageEvidence identifies a decoded output without exposing its contents.
type ImageEvidence struct {
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	MediaType string `json:"media_type"`
}

func imageProvider(provider string) bool {
	return provider == "openai-images" || provider == "gemini-images"
}

func checkImages(ctx context.Context, c Config, g imagegen.Generator, result *Result) error {
	if g == nil {
		return errors.New("no image generator")
	}
	req := &imagegen.Request{Model: c.Model, Prompt: "A simple blue circle on a white background.", N: 1,
		Size: "1024x1024", Quality: "low", OutputFormat: "png"}
	if c.Provider == "gemini-images" {
		req.Size, req.Quality, req.AspectRatio = "1K", "", "1:1"
	}
	var resp *imagegen.Response
	var err error
	switch result.Name {
	case "generate":
		resp, err = g.Generate(ctx, req)
	case "edit":
		e, ok := g.(imagegen.Editor)
		if !ok {
			return errors.New("no image editor")
		}
		req.Prompt = "Add a small blue circle to this image."
		resp, err = e.Edit(ctx, &imagegen.EditRequest{Request: *req, Images: []model.Part{*c.Media}})
	case "stream":
		s, ok := g.(imagegen.Streamer)
		if !ok {
			return errors.New("no image streamer")
		}
		req.PartialImages = 1
		for ev, streamErr := range s.Stream(ctx, req) {
			if streamErr != nil {
				return streamErr
			}
			if ev == nil {
				return errors.New("nil image stream event")
			}
			if ev.Response != nil {
				if resp != nil {
					return errors.New("duplicate image completion")
				}
				resp = ev.Response
			}
		}
	default:
		return errors.New("unknown images check")
	}
	if err != nil {
		return err
	}
	if resp == nil || len(resp.Images) != 1 {
		return errors.New("missing completed image")
	}
	for _, img := range resp.Images {
		part := img.Part
		cfg, format, decodeErr := image.DecodeConfig(bytes.NewReader(part.Data))
		if decodeErr != nil || cfg.Width <= 0 || cfg.Height <= 0 || format != "png" || part.MediaType != "image/png" {
			return errors.New("invalid image output")
		}
		if int64(cfg.Width)*int64(cfg.Height) > 64<<20 {
			return errors.New("image output exceeds decoded pixel budget")
		}
		if _, _, err := image.Decode(bytes.NewReader(part.Data)); err != nil {
			return errors.New("incomplete image output")
		}
		hash := sha256.Sum256(part.Data)
		result.Images = append(result.Images, ImageEvidence{SHA256: hex.EncodeToString(hash[:]), Bytes: len(part.Data), Width: cfg.Width, Height: cfg.Height, MediaType: part.MediaType})
	}
	result.ImageUsage, result.RequestID = resp.Usage, resp.RequestID
	return nil
}
