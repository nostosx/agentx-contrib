// Package browser implements isolated Chromium sessions through chromedp.
package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cdbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"

	core "github.com/nostosx/agentx/browser"
	"github.com/nostosx/agentx/web"
)

// Options configures trusted browser startup. Each session starts its own
// temporary Chromium process/profile, never the user's logged-in browser.
// Browser network isolation belongs to the application's container/egress
// policy. CheckURL validates explicit navigation, not arbitrary page scripts.
type Options struct {
	Binary         string
	Extractor      web.Extractor
	Timeout        time.Duration
	MaxHTMLBytes   int
	MaxResultBytes int
	NoSandbox      bool
	CheckURL       func(context.Context, string) error
}

// Browser creates independent, application-owned sessions.
type Browser struct{ o Options }

// New configures a Chromium driver; it neither starts nor downloads a browser.
func New(o Options) (*Browser, error) {
	if o.Extractor == nil {
		return nil, errors.New("browser: an HTML extractor is required")
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxHTMLBytes == 0 {
		o.MaxHTMLBytes = 4 << 20
	}
	if o.MaxResultBytes == 0 {
		o.MaxResultBytes = 8 << 20
	}
	if o.Timeout < 0 || o.MaxHTMLBytes < 1 || o.MaxHTMLBytes > 32<<20 || o.MaxResultBytes < 1 || o.MaxResultBytes > 32<<20 {
		return nil, errors.New("browser: invalid limits")
	}
	if o.Binary != "" {
		if _, err := os.Stat(o.Binary); err != nil {
			return nil, errors.New("browser: configured Chromium binary is unavailable")
		}
	}
	return &Browser{o: o}, nil
}

// Session serializes operations on one private page and owns its browser.
type Session struct {
	o           Options
	ctx         context.Context
	cancel      context.CancelFunc
	allocCancel context.CancelFunc
	gate        chan struct{}
	once        sync.Once
	closeErr    error
}

// NewSession starts a private browser bound to the owner context. A failed
// allocation cleans up its process and temporary profile before returning.
func (b *Browser) NewSession(ctx context.Context) (core.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := append([]chromedp.ExecAllocatorOption(nil), chromedp.DefaultExecAllocatorOptions[:]...)
	if b.o.Binary != "" {
		options = append(options, chromedp.ExecPath(b.o.Binary))
	}
	if b.o.NoSandbox {
		options = append(options, chromedp.NoSandbox)
	}
	options = append(options, chromedp.Flag("disable-background-networking", true), chromedp.Flag("disable-sync", true))
	alloc, allocCancel := chromedp.NewExecAllocator(ctx, options...)
	owner, cancel := chromedp.NewContext(alloc)
	s := &Session{o: b.o, ctx: owner, cancel: cancel, allocCancel: allocCancel, gate: make(chan struct{}, 1)}
	// The allocation call uses the owner context: a per-action timeout must not
	// become the browser's lifetime. A watchdog cancels a stalled allocation.
	timer := time.AfterFunc(b.o.Timeout, cancel)
	_, err := chromedp.Run(owner, chromedp.Location())
	timer.Stop()
	if err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("browser: start Chromium: %w", err)
	}
	_, err = chromedp.Call(owner, cdbrowser.SetDownloadBehavior, cdbrowser.SetDownloadBehaviorParams{Behavior: cdbrowser.SetDownloadBehaviorBehaviorDeny})
	if err != nil {
		cancel()
		allocCancel()
		return nil, fmt.Errorf("browser: deny automatic downloads: %w", err)
	}
	return s, nil
}

// Do implements core.Session. A cancelled/timed-out action closes this session
// so a previously-issued navigation or script cannot keep running afterwards.
func (s *Session) Do(ctx context.Context, a core.Action) (*core.Result, error) {
	if err := a.Validate(); err != nil {
		return nil, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	op, cancel := context.WithTimeout(s.ctx, s.o.Timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	result, err := s.perform(op, a)
	if err != nil && a.Operation == core.Download {
		s.cancel()
		s.allocCancel()
	}
	if op.Err() != nil {
		s.cancel()
		s.allocCancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, op.Err()
	}
	return result, err
}

func (s *Session) perform(ctx context.Context, a core.Action) (*core.Result, error) {
	switch a.Operation {
	case core.Navigate:
		if s.o.CheckURL != nil {
			if err := s.o.CheckURL(ctx, a.URL); err != nil {
				return nil, err
			}
		}
		if err := chromedp.Do(ctx, chromedp.Navigate(a.URL)); err != nil {
			return nil, err
		}
	case core.Click:
		if err := chromedp.Do(ctx, chromedp.Click(chromedp.CSS(a.Selector))); err != nil {
			return nil, err
		}
	case core.Fill:
		if err := chromedp.Do(ctx, chromedp.SetValue(chromedp.CSS(a.Selector), a.Value)); err != nil {
			return nil, err
		}
	case core.Screenshot:
		data, err := chromedp.Run(ctx, chromedp.FullScreenshot(100))
		if err != nil {
			return nil, err
		}
		if len(data) > s.o.MaxResultBytes {
			return nil, errors.New("browser: screenshot exceeds limit")
		}
		return &core.Result{Name: "screenshot.png", MediaType: "image/png", Data: data}, nil
	case core.Download:
		return s.download(ctx, a.Selector)
	}
	return s.snapshot(ctx)
}

func (s *Session) snapshot(ctx context.Context) (*core.Result, error) {
	raw, err := chromedp.Run(ctx, chromedp.Location())
	if err != nil {
		return nil, err
	}
	if err := web.ValidateURL(raw); err != nil {
		return nil, err
	}
	markup, err := chromedp.Run(ctx, chromedp.OuterHTML(chromedp.CSS("html")))
	if err != nil {
		return nil, err
	}
	if len(markup) > s.o.MaxHTMLBytes {
		return nil, errors.New("browser: HTML exceeds limit")
	}
	doc, err := s.o.Extractor.Extract(ctx, raw, []byte(markup))
	if err != nil {
		return nil, err
	}
	const script = `Array.from(document.querySelectorAll('a,button,input,textarea,select')).slice(0,100).map(e=>{let p=e,parts=[];while(p&&p.nodeType===1){if(p.id&&document.querySelectorAll('#'+CSS.escape(p.id)).length===1){parts.unshift('#'+CSS.escape(p.id));break;}let n=1;for(let q=p.previousElementSibling;q;q=q.previousElementSibling)if(q.tagName===p.tagName)n++;parts.unshift(p.tagName.toLowerCase()+':nth-of-type('+n+')');p=p.parentElement;}return {selector:parts.join(' > '),tag:e.tagName.toLowerCase(),text:(e.getAttribute('aria-label')||e.getAttribute('placeholder')||e.textContent||'').trim().slice(0,160),type:e.getAttribute('type')||''};})`
	controls, err := chromedp.Run(ctx, chromedp.Evaluate[[]core.Control](script))
	if err != nil {
		return nil, err
	}
	return &core.Result{Document: doc, Controls: controls}, nil
}

func (s *Session) download(ctx context.Context, selector string) (*core.Result, error) {
	dir, err := os.MkdirTemp("", "agentx-browser-download-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	progress := chromedp.Events(ctx, cdbrowser.DownloadProgress)
	_, err = chromedp.Call(ctx, cdbrowser.SetDownloadBehavior, cdbrowser.SetDownloadBehaviorParams{Behavior: cdbrowser.SetDownloadBehaviorBehaviorAllowAndName, DownloadPath: dir, EventsEnabled: new(true)})
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = chromedp.Call(ctx, cdbrowser.SetDownloadBehavior, cdbrowser.SetDownloadBehaviorParams{Behavior: cdbrowser.SetDownloadBehaviorBehaviorDeny})
	}()
	if err := chromedp.Do(ctx, chromedp.Click(chromedp.CSS(selector))); err != nil {
		return nil, err
	}
	for event, eventErr := range progress {
		if eventErr != nil {
			return nil, eventErr
		}
		if event.TotalBytes > float64(s.o.MaxResultBytes) || event.ReceivedBytes > float64(s.o.MaxResultBytes) {
			_, _ = chromedp.Call(ctx, cdbrowser.CancelDownload, cdbrowser.CancelDownloadParams{GUID: event.GUID})
			return nil, errors.New("browser: download exceeds limit")
		}
		switch event.State {
		case cdbrowser.DownloadProgressStateCanceled:
			return nil, errors.New("browser: download cancelled")
		case cdbrowser.DownloadProgressStateCompleted:
			if event.GUID == "" || filepath.Base(event.GUID) != event.GUID || strings.ContainsAny(event.GUID, "/\\") {
				return nil, errors.New("browser: invalid download identity")
			}
			f, err := os.Open(filepath.Join(dir, event.GUID))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(io.LimitReader(f, int64(s.o.MaxResultBytes)+1))
			closeErr := f.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			if len(data) > s.o.MaxResultBytes {
				return nil, errors.New("browser: download exceeds limit")
			}
			return &core.Result{Name: "download-" + event.GUID, MediaType: "application/octet-stream", Data: data}, nil
		}
	}
	return nil, errors.New("browser: download ended without completion")
}

//go:fix inline

// Close stops only this session's process and waits for profile cleanup.
func (s *Session) Close() error {
	s.once.Do(func() {
		s.closeErr = chromedp.Cancel(s.ctx)
		s.cancel()
		s.allocCancel()
		if errors.Is(s.closeErr, context.Canceled) {
			s.closeErr = nil
		}
	})
	return s.closeErr
}

var _ core.Browser = (*Browser)(nil)
var _ core.Session = (*Session)(nil)
