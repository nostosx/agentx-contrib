// Package embedding composes embedding models with servex cache infrastructure.
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/nostosx/servex-contrib/cache/loader"
	"github.com/nostosx/servex/encoding"
	"github.com/nostosx/servex/storage/cache"

	"github.com/nostosx/agentx/memory"
)

// CacheOptions defines vector-space identity and the shared-load lifecycle.
type CacheOptions struct {
	// Namespace must identify tenant/access scope, model version, dimensions
	// and task type. Query/document spaces must not accidentally share keys.
	Namespace   string
	TTL         time.Duration
	LoadTimeout time.Duration
	// Codec is injected explicitly, for example servex-contrib/encoding/json.Codec.
	Codec      encoding.Codec
	Dimensions int
}

// Cached implements memory.Embedder. Hits are reused per text; unique misses
// remain one batch. Identical concurrent miss batches share servex's Loader.
// Backend cache errors are best-effort misses. Values returned to callers are
// copied. Close joins shared loads and does not close the cache or embedder.
type Cached struct {
	next   memory.Embedder
	cache  cache.Cache
	loader *loader.Loader[[][]float32]
	opts   CacheOptions
	owner  context.Context
	cancel context.CancelCauseFunc
}

var _ memory.Embedder = (*Cached)(nil)

// NewCached constructs a lifecycle-owned cache adapter, reusing servex's cache
// and shared Loader rather than implementing a cache or singleflight backend.
func NewCached(ctx context.Context, next memory.Embedder, backend cache.Cache, opts CacheOptions) (*Cached, error) {
	if ctx == nil || next == nil || backend == nil || opts.Codec == nil || strings.TrimSpace(opts.Namespace) == "" || opts.Dimensions < 0 {
		return nil, errors.New("embedding: owner context, embedder, cache, codec and namespace are required")
	}
	if opts.LoadTimeout == 0 {
		opts.LoadTimeout = 30 * time.Second
	}
	owner, cancel := context.WithCancelCause(ctx)
	l, err := loader.New[[][]float32](owner, backend, opts.LoadTimeout, loader.WithCodec(opts.Codec), loader.WithTTL(opts.TTL))
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("embedding: cache loader: %w", err)
	}
	return &Cached{next: next, cache: backend, loader: l, opts: opts, owner: owner, cancel: cancel}, nil
}

func (e *Cached) key(text string) string {
	b, _ := json.Marshal([]string{"embedding-v1", e.opts.Namespace, text})
	sum := sha256.Sum256(b)
	return "agentx:embedding:" + hex.EncodeToString(sum[:])
}

func valid(vector []float32, dimensions int) bool {
	if len(vector) == 0 || dimensions > 0 && len(vector) != dimensions {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

// Embed returns vectors in input order, preserving duplicates without duplicate
// upstream inputs. Failed upstream batches are never cached or partially returned.
func (e *Cached) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := context.Cause(e.owner); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	if slices.Contains(texts, "") {
		return nil, errors.New("embedding: cannot embed empty text")
	}
	out := make([][]float32, len(texts))
	keys := make([]string, len(texts))
	var missing, missKeys []string
	positions := map[string]int{}
	for i, text := range texts {
		key := e.key(text)
		keys[i] = key
		data, err := e.cache.Get(ctx, key)
		if err == nil {
			var vector []float32
			if e.opts.Codec.Unmarshal(data, &vector) == nil && valid(vector, e.opts.Dimensions) {
				out[i] = slices.Clone(vector)
				continue
			}
		}
		if _, ok := positions[key]; !ok {
			positions[key] = len(missing)
			missing, missKeys = append(missing, text), append(missKeys, key)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(missing) != 0 {
		batchData, _ := json.Marshal(missKeys)
		sum := sha256.Sum256(batchData)
		batchKey := "agentx:embedding:batch:" + hex.EncodeToString(sum[:])
		vectors, err := e.loader.Get(ctx, batchKey, func(loadCtx context.Context) ([][]float32, error) {
			vectors, err := e.next.Embed(loadCtx, missing)
			if err != nil {
				return nil, err
			}
			if err := e.validate(vectors, len(missing)); err != nil {
				return nil, err
			}
			if err := loadCtx.Err(); err != nil {
				return nil, err
			}
			for i, vector := range vectors {
				data, err := e.opts.Codec.Marshal(vector)
				if err == nil {
					_ = e.cache.Set(loadCtx, missKeys[i], data, e.opts.TTL)
				}
			}
			return vectors, nil
		})
		if err != nil {
			return nil, err
		}
		if err := e.validate(vectors, len(missing)); err != nil {
			_ = e.cache.Delete(ctx, batchKey)
			return nil, err
		}
		for i, key := range keys {
			if out[i] == nil {
				out[i] = slices.Clone(vectors[positions[key]])
			}
		}
	}
	if err := e.validate(out, len(texts)); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *Cached) validate(vectors [][]float32, count int) error {
	if len(vectors) != count {
		return errors.New("embedding: vector count mismatch")
	}
	dimensions := e.opts.Dimensions
	for _, vector := range vectors {
		if !valid(vector, dimensions) {
			return errors.New("embedding: invalid cached or generated vector")
		}
		if dimensions == 0 {
			dimensions = len(vector)
		}
	}
	return nil
}

// Close cancels and joins shared loads; it does not close external dependencies.
func (e *Cached) Close() error {
	e.cancel(loader.ErrLoaderClosed)
	return e.loader.Close()
}
