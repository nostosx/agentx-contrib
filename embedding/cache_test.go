package embedding_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachememory "github.com/nostosx/servex-contrib/cache/memory"
	jsoncodec "github.com/nostosx/servex-contrib/encoding/json"
	"github.com/nostosx/servex/storage/cache"

	"github.com/nostosx/agentx-contrib/embedding"
)

type embedFunc func(context.Context, []string) ([][]float32, error)

func (f embedFunc) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f(ctx, texts)
}

func options(namespace string) embedding.CacheOptions {
	return embedding.CacheOptions{Namespace: namespace, Codec: jsoncodec.Codec, Dimensions: 2, LoadTimeout: time.Second}
}

func TestCached(t *testing.T) {
	t.Parallel()
	backend := cachememory.New()
	var batches [][]string
	next := embedFunc(func(_ context.Context, texts []string) ([][]float32, error) {
		batches = append(batches, append([]string(nil), texts...))
		vectors := make([][]float32, len(texts))
		for i, text := range texts {
			vectors[i] = []float32{float32(len(text)), 1}
		}
		return vectors, nil
	})
	e, err := embedding.NewCached(t.Context(), next, backend, options("tenant/model/v1"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	out, err := e.Embed(t.Context(), []string{"a", "bb", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, [][]float32{{1, 1}, {2, 1}, {1, 1}}) || !reflect.DeepEqual(batches, [][]string{{"a", "bb"}}) {
		t.Fatalf("vectors=%v batches=%v", out, batches)
	}
	out[0][0] = 99
	out, err = e.Embed(t.Context(), []string{"a", "ccc", "bb"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0][0] != 1 || !reflect.DeepEqual(batches, [][]string{{"a", "bb"}, {"ccc"}}) {
		t.Fatalf("cache=%v batches=%v", out, batches)
	}
	other, err := embedding.NewCached(t.Context(), next, backend, options("other/model/v1"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Embed(t.Context(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if len(batches) != 3 {
		t.Fatal("namespaces collided")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Embed(t.Context(), []string{"a"}); err == nil {
		t.Fatal("closed cache accepted a hit")
	}
}

func TestConcurrentBatch(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	next := embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return [][]float32{{1, 2}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	e, err := embedding.NewCached(t.Context(), next, cachememory.New(), options("shared"))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			out, err := e.Embed(t.Context(), []string{"a"})
			if err != nil || len(out) != 1 {
				t.Errorf("Embed=%v %v", out, err)
			}
		})
	}
	<-started
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("shared calls=%d", calls.Load())
	}
}

type failedCache struct{ cache.Cache }

func (f failedCache) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("cache down")
}
func (f failedCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("cache down")
}

type failedCodec struct{}

func (failedCodec) Name() string                { return "failed" }
func (failedCodec) Marshal(any) ([]byte, error) { return nil, errors.New("codec down") }
func (failedCodec) Unmarshal([]byte, any) error { return errors.New("codec down") }

func TestCodecFailureIsBestEffort(t *testing.T) {
	t.Parallel()
	opts := options("codec")
	opts.Codec = failedCodec{}
	e, err := embedding.NewCached(t.Context(), embedFunc(func(context.Context, []string) ([][]float32, error) { return [][]float32{{1, 2}}, nil }), cachememory.New(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if out, err := e.Embed(t.Context(), []string{"a"}); err != nil || len(out) != 1 {
		t.Fatalf("cache encoding lost model output: %v, %v", out, err)
	}
}

func TestCacheFailureAndInvalidVectors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		vectors [][]float32
		fail    bool
	}{
		{name: "healthy", vectors: [][]float32{{1, 2}}},
		{name: "count mismatch", vectors: [][]float32{}, fail: true},
		{name: "invalid number", vectors: [][]float32{{float32(math.NaN()), 2}}, fail: true},
		{name: "dimension mismatch", vectors: [][]float32{{1}}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := embedding.NewCached(t.Context(), embedFunc(func(context.Context, []string) ([][]float32, error) { return tc.vectors, nil }), failedCache{}, options("scope"))
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			_, err = e.Embed(t.Context(), []string{"a"})
			if (err != nil) != tc.fail {
				t.Fatalf("Embed=%v", err)
			}
		})
	}
}

func TestCancelledWaiterAndClose(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	e, err := embedding.NewCached(t.Context(), embedFunc(func(ctx context.Context, _ []string) ([][]float32, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}), cachememory.New(), options("cancel"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := e.Embed(ctx, []string{"a"}); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter=%v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}
