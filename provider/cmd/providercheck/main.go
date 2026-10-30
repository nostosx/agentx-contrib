// Command providercheck explicitly opts into bounded live-provider acceptance.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/nostosx/agentx-contrib/provider/internal/acceptance"
	"github.com/nostosx/agentx/model"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout)
	cancel()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out io.Writer) int {
	var c acceptance.Config
	var checks, mediaPath, mediaType string
	var timeout time.Duration
	fs := flag.NewFlagSet("providercheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&c.Live, "live", false, "enable paid network requests using configured provider credentials")
	fs.StringVar(&c.Provider, "provider", "", "openai, openai-chat, openai-images, gemini-images, anthropic, gemini or bedrock")
	fs.StringVar(&c.Model, "model", "", "explicit generation model; no implicit default")
	fs.StringVar(&c.EmbeddingModel, "embedding-model", "", "explicit embedding model when requesting embedding")
	fs.StringVar(&c.BaseURL, "base-url", "", "optional adapter API endpoint")
	fs.StringVar(&checks, "checks", "", "comma-separated checks: generate,stream,tools,structured,embedding,media,edit; default generate,stream for image providers, otherwise generate,stream,tools,structured")
	fs.IntVar(&c.MaxTokens, "max-tokens", 512, "output-token limit per generation request, 64 to 4096")
	fs.DurationVar(&c.Timeout, "request-timeout", 30*time.Second, "per-check deadline, at most 5m")
	fs.DurationVar(&timeout, "timeout", 3*time.Minute, "overall deadline, at most 30m")
	fs.StringVar(&mediaPath, "media", "", "optional inline media fixture, at most 5 MiB")
	fs.StringVar(&mediaType, "media-type", "", "fixture MIME type, required with -media")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(out)
			fs.PrintDefaults()
			return 0
		}
		return writeReport(out, acceptance.Report{Time: time.Now().UTC(), Status: "unverified", ErrorCode: "invalid_arguments"})
	}
	if fs.NArg() != 0 || timeout <= 0 || timeout > 30*time.Minute {
		return writeReport(out, acceptance.Report{Time: time.Now().UTC(), Status: "unverified", ErrorCode: "invalid_arguments"})
	}
	if checks != "" {
		c.Checks = strings.Split(checks, ",")
	}
	if c.Live && mediaPath != "" {
		media, err := readMedia(mediaPath, mediaType)
		if err != nil {
			return writeReport(out, acceptance.Report{Time: time.Now().UTC(), Provider: c.Provider, Model: c.Model, Status: "unverified", ErrorCode: "invalid_media_fixture"})
		}
		c.Media = media
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return writeReport(out, acceptance.Run(ctx, c, acceptance.Providers))
}

func writeReport(out io.Writer, report acceptance.Report) int {
	if err := json.NewEncoder(out).Encode(report); err != nil {
		return 2
	}
	if report.Status != "passed" {
		return 1
	}
	return 0
}

func readMedia(path, mediaType string) (*model.Part, error) {
	if mediaType == "" {
		return nil, os.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxBytes = 5 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxBytes {
		return nil, os.ErrInvalid
	}
	p := model.MediaPart(mediaType, data)
	p.Filename = filepath.Base(path)
	return &p, nil
}
