// Package loader implements scoped, read-only cloud document connectors.
package loader

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nostosx/servex-contrib/transport/httpx"
	"github.com/nostosx/servex/encoding"
	"github.com/nostosx/servex/endpoint"
	"github.com/nostosx/servex/metadata"
	"github.com/nostosx/servex/storage/cache"

	"github.com/nostosx/agentx/rag"
)

// ErrCursorExpired requires a new baseline; it never means an empty snapshot.
var ErrCursorExpired = errors.New("loader: cursor missing, expired or outside scope")

// Options selects trusted credentials, transport and bounded snapshot state.
// Scope must include the tenant and authorization domain. Cache and Codec are
// borrowed Servex facilities, required only for LoadChanges. No retry is added.
type Options struct {
	Endpoint     string
	Token        func(context.Context) (string, error)
	Client       *http.Client
	Scope        string
	Cache        cache.Cache
	Codec        encoding.Codec
	CursorTTL    time.Duration
	MaxDocuments int
	MaxBytes     int
	MaxPages     int
}

// Connector implements full snapshots and snapshot-derived deltas. LoadChanges
// fetches the complete configured scope, not a provider event/change feed.
// Persist its new cursor only after Index.ApplyChanges succeeds. Never Sync a delta.
type Connector struct {
	o       Options
	client  *httpx.Client
	http    *http.Client
	owned   bool
	key     string
	headers []string
	load    func(context.Context) ([]*rag.Document, error)
}

func newConnector(o Options, endpoint string, config any) (*Connector, error) {
	if o.Endpoint == "" {
		o.Endpoint = endpoint
	}
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("loader: invalid endpoint")
	}
	if o.MaxDocuments == 0 {
		o.MaxDocuments = 1000
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 4 << 20
	}
	if o.MaxPages == 0 {
		o.MaxPages = 1000
	}
	if o.CursorTTL == 0 {
		o.CursorTTL = 24 * time.Hour
	}
	if o.MaxDocuments < 1 || o.MaxDocuments > 100000 || o.MaxBytes < 1 || o.MaxBytes > 32<<20 || o.MaxPages < 1 || o.MaxPages > 10000 || o.CursorTTL < time.Second || o.CursorTTL > 30*24*time.Hour {
		return nil, errors.New("loader: invalid limits")
	}
	if (o.Cache == nil) != (o.Codec == nil) || (o.Cache != nil && o.Scope == "") {
		return nil, errors.New("loader: cache, codec and scope must be configured together")
	}
	c := &Connector{o: o}
	var hc *http.Client
	if o.Client == nil {
		hc, err = httpx.NewRestrictedHTTPClient(httpx.HTTPPolicy{MaxResponseBytes: int64(o.MaxBytes)})
		c.owned = true
		if err != nil {
			return nil, err
		}
	} else {
		copy := *o.Client
		hc = &copy
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if hc.Timeout == 0 {
		hc.Timeout = 30 * time.Second
	}
	c.http = hc
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = limitedTransport{base, int64(o.MaxBytes)}
	c.client, err = httpx.NewClient(context.Background(), o.Endpoint, httpx.WithHTTPClient(hc))
	if err != nil {
		if c.owned {
			hc.CloseIdleConnections()
		}
		return nil, err
	}
	data, err := json.Marshal([]any{o.Endpoint, o.Scope, config})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	c.key = "agentx:loader:" + hex.EncodeToString(sum[:]) + ":"
	return c, nil
}

func (c *Connector) get(ctx context.Context, path string, query any, out any) error {
	headers := slices.Clone(c.headers)
	if c.o.Token != nil {
		token, err := c.o.Token(ctx)
		if err != nil {
			return errors.New("loader: credential lookup failed")
		}
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return errors.New("loader: invalid credential")
		}
		headers = append(headers, "Authorization", "Bearer "+token)
	}
	setHeaders := func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, in any) (any, error) {
			info, ok := metadata.FromContext(ctx)
			if !ok {
				return nil, errors.New("loader: missing outbound metadata")
			}
			for i := 0; i < len(headers); i += 2 {
				info.Header.Set(headers[i], headers[i+1])
			}
			return next(ctx, in)
		}
	}
	result, err := httpx.Call[json.RawMessage](c.client, http.MethodGet, path, setHeaders)(ctx, query)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("loader: source request failed; snapshot incomplete")
	}
	data, ok := result.(*json.RawMessage)
	if !ok || len(*data) > c.o.MaxBytes {
		return errors.New("loader: invalid or oversized source response")
	}
	if err := json.Unmarshal(*data, out); err != nil {
		return errors.New("loader: malformed source response")
	}
	return nil
}

// Load fails as a whole on any permission, pagination, parsing or size error.
func (c *Connector) Load(ctx context.Context) ([]*rag.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	docs, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	if len(docs) > c.o.MaxDocuments {
		return nil, errors.New("loader: snapshot document limit exceeded")
	}
	total := 0
	seen := map[string]bool{}
	for _, doc := range docs {
		if doc == nil || doc.Source == "" || seen[doc.Source] {
			return nil, errors.New("loader: invalid or duplicate source")
		}
		seen[doc.Source] = true
		total += len(doc.Content)
		if total > c.o.MaxBytes {
			return nil, errors.New("loader: snapshot byte limit exceeded")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

type manifest struct {
	Hashes map[string]string `json:"hashes"`
}

// LoadChanges compares complete snapshots using an opaque, expiring Servex
// cache manifest. A failed snapshot creates no cursor or inferred deletion.
func (c *Connector) LoadChanges(ctx context.Context, cursor string) (*rag.Changes, error) {
	if c.o.Cache == nil {
		return nil, errors.New("loader: LoadChanges requires cache, codec and scope")
	}
	old := manifest{Hashes: map[string]string{}}
	if cursor != "" {
		if !cursorPattern.MatchString(cursor) {
			return nil, ErrCursorExpired
		}
		data, err := c.o.Cache.Get(ctx, c.key+cursor)
		if errors.Is(err, cache.ErrNotFound) {
			return nil, ErrCursorExpired
		}
		if err != nil {
			return nil, err
		}
		if c.o.Codec.Unmarshal(data, &old) != nil || old.Hashes == nil {
			return nil, ErrCursorExpired
		}
	}
	docs, err := c.Load(ctx)
	if err != nil {
		return nil, err
	}
	next := manifest{Hashes: map[string]string{}}
	changes := &rag.Changes{}
	for _, doc := range docs {
		data, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		hash := hex.EncodeToString(sum[:])
		next.Hashes[doc.Source] = hash
		if old.Hashes[doc.Source] != hash {
			changes.Documents = append(changes.Documents, doc)
		}
	}
	for source := range old.Hashes {
		if _, ok := next.Hashes[source]; !ok {
			changes.DeletedSources = append(changes.DeletedSources, source)
		}
	}
	slices.Sort(changes.DeletedSources)
	id := make([]byte, 24)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	changes.Cursor = hex.EncodeToString(id)
	data, err := c.o.Codec.Marshal(next)
	if err != nil {
		return nil, err
	}
	if err := c.o.Cache.Set(ctx, c.key+changes.Cursor, data, c.o.CursorTTL); err != nil {
		return nil, err
	}
	return changes, nil
}

var cursorPattern = regexp.MustCompile(`^[a-f0-9]{48}$`)
var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type limitedTransport struct {
	base http.RoundTripper
	max  int64
}

func (t limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	r.Body = http.MaxBytesReader(nil, r.Body, t.max)
	return r, nil
}
func (t limitedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// Close releases this connector's discovery and owned HTTP idle connections.
func (c *Connector) Close() error {
	err := c.client.Close()
	if c.owned {
		c.http.CloseIdleConnections()
	}
	return err
}

func document(source, title, content, webURL string) *rag.Document {
	return &rag.Document{ID: source, Source: source, Content: content, Metadata: map[string]any{rag.MetaTitle: title, rag.MetaURL: webURL, rag.MetaFormat: "markdown"}}
}
func limitError() error { return fmt.Errorf("loader: pagination, depth or document limit exceeded") }

var _ rag.Loader = (*Connector)(nil)
var _ rag.ChangeLoader = (*Connector)(nil)
