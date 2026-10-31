package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/nostosx/agentx/rag"
)

// WriterOptions configures non-atomic source replacement. The application
// creates the index and serializes separate writers in the same namespace.
// Dimensions is required for Qdrant's manifest vector (also with named vectors).
// Elasticsearch must map namespace, agentx_record, source_key and chunk.source
// as keyword. No embedding requests are made by a Writer.
type WriterOptions struct {
	Options
	Dimensions int
	BatchSize  int
	MaxSources int
}

// Writer persists a pending manifest before changing chunks, and commits its
// hash only after all batches succeed. Readers may observe partial replacement;
// a pending hash is empty and forces the next Index.Add/Sync to retry.
type Writer struct {
	r    *Retriever
	o    WriterOptions
	gate chan struct{}
}

// NewQdrantWriter writes a pre-created cosine collection.
func NewQdrantWriter(o WriterOptions) (*Writer, error) { return newWriter(o, "qdrant") }

// NewElasticsearchWriter writes a pre-created text/vector index.
func NewElasticsearchWriter(o WriterOptions) (*Writer, error) { return newWriter(o, "elasticsearch") }
func newWriter(o WriterOptions, provider string) (*Writer, error) {
	if o.BatchSize == 0 {
		o.BatchSize = 100
	}
	if o.MaxSources == 0 {
		o.MaxSources = 100000
	}
	if o.BatchSize < 1 || o.BatchSize > 1000 || o.MaxSources < 1 || o.MaxSources > 1000000 || o.Dimensions < 0 || o.Dimensions > 65536 || (provider == "qdrant" && o.Dimensions == 0) {
		return nil, errors.New("retrieval: invalid writer limits or missing Qdrant dimensions")
	}
	r, err := newRetriever(o.Options, provider)
	if err != nil {
		return nil, err
	}
	return &Writer{r: r, o: o, gate: make(chan struct{}, 1)}, nil
}
func (w *Writer) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case w.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (w *Writer) unlock() { <-w.gate }

func (w *Writer) id(kind, source, id string) string {
	data, _ := json.Marshal([]string{w.r.o.Namespace, kind, source, id})
	sum := sha256.Sum256(data)
	s := hex.EncodeToString(sum[:])
	if w.r.provider == "qdrant" {
		return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
	}
	return s
}
func (w *Writer) validate(src rag.SourceChunks) error {
	if src.Info.Source == "" || src.Info.Hash == "" || src.Info.Chunks != len(src.Chunks) {
		return errors.New("retrieval: source, hash and accurate chunk count required")
	}
	seen := map[string]bool{}
	for _, c := range src.Chunks {
		if c == nil || c.ID == "" || c.Source != src.Info.Source || seen[c.ID] {
			return errors.New("retrieval: invalid source chunk identity")
		}
		seen[c.ID] = true
		if (w.r.provider == "qdrant" || w.o.Dimensions > 0) && len(c.Embedding) != w.o.Dimensions {
			return errors.New("retrieval: embedding dimension mismatch")
		}
		var norm float64
		for _, v := range c.Embedding {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return errors.New("retrieval: nonfinite embedding")
			}
			norm += float64(v) * float64(v)
		}
		if len(c.Embedding) > 0 && norm == 0 {
			return errors.New("retrieval: zero embedding")
		}
	}
	_, err := json.Marshal(src)
	return err
}

// Write validates the entire input before mutation. Successful preceding sources
// remain committed if a later source fails. Retry the failed/unconfirmed inputs.
func (w *Writer) Write(ctx context.Context, sources ...rag.SourceChunks) error {
	seen := map[string]bool{}
	for _, src := range sources {
		if seen[src.Info.Source] {
			return errors.New("retrieval: duplicate source")
		}
		seen[src.Info.Source] = true
		if err := w.validate(src); err != nil {
			return err
		}
	}
	if err := w.lock(ctx); err != nil {
		return err
	}
	defer w.unlock()
	for _, src := range sources {
		pending := src.Info
		pending.Hash = ""
		pending.Chunks = 0
		if err := w.manifest(ctx, pending); err != nil {
			return err
		}
		if err := w.removeChunks(ctx, src.Info.Source); err != nil {
			return err
		}
		for start := 0; start < len(src.Chunks); start += w.o.BatchSize {
			end := min(start+w.o.BatchSize, len(src.Chunks))
			if err := w.writeChunks(ctx, src.Chunks[start:end]); err != nil {
				return err
			}
		}
		if err := w.manifest(ctx, src.Info); err != nil {
			return err
		}
	}
	return nil
}

// Delete is idempotent. A failure leaves a pending source for a safe retry.
func (w *Writer) Delete(ctx context.Context, sources ...string) error {
	if slices.Contains(sources, "") {
		return errors.New("retrieval: empty source")
	}
	if err := w.lock(ctx); err != nil {
		return err
	}
	defer w.unlock()
	for _, source := range sources {
		if err := w.manifest(ctx, rag.SourceInfo{Source: source}); err != nil {
			return err
		}
		if err := w.removeChunks(ctx, source); err != nil {
			return err
		}
		if w.r.provider == "qdrant" {
			if err := w.qmutation(ctx, "points/delete", map[string]any{"points": []string{w.id("manifest", source, "")}}); err != nil {
				return err
			}
		} else {
			var result map[string]any
			if err := w.esBulk(ctx, []map[string]any{{"delete": map[string]any{"_id": w.id("manifest", source, "")}}}, &result); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *Writer) payload(info rag.SourceInfo, c *rag.Chunk) map[string]any {
	p := map[string]any{"namespace": w.r.o.Namespace}
	if c == nil {
		p["agentx_record"] = "manifest"
		p["source_info"] = info
		p["source_key"] = info.Source
	} else {
		p["agentx_record"] = "chunk"
		p["chunk"] = c
		if len(c.Embedding) > 0 {
			p["embedding"] = c.Embedding
		}
	}
	return p
}
func (w *Writer) qvector(v []float32) any {
	if w.r.o.VectorName != "" {
		return map[string]any{w.r.o.VectorName: v}
	}
	return v
}
func (w *Writer) manifest(ctx context.Context, info rag.SourceInfo) error {
	p := w.payload(info, nil)
	id := w.id("manifest", info.Source, "")
	if w.r.provider == "qdrant" {
		v := make([]float32, w.o.Dimensions)
		v[0] = 1
		return w.qmutation(ctx, "points", map[string]any{"points": []any{map[string]any{"id": id, "vector": w.qvector(v), "payload": p}}})
	}
	return w.esBulk(ctx, []map[string]any{{"index": map[string]any{"_id": id}}, p}, nil)
}
func (w *Writer) writeChunks(ctx context.Context, chunks []*rag.Chunk) error {
	var rows []map[string]any
	for _, c := range chunks {
		p := w.payload(rag.SourceInfo{}, c)
		id := w.id("chunk", c.Source, c.ID)
		if w.r.provider == "qdrant" {
			delete(p, "embedding")
			rows = append(rows, map[string]any{"id": id, "vector": w.qvector(c.Embedding), "payload": p})
		} else {
			rows = append(rows, map[string]any{"index": map[string]any{"_id": id}}, p)
		}
	}
	if w.r.provider == "qdrant" {
		return w.qmutation(ctx, "points", map[string]any{"points": rows})
	}
	return w.esBulk(ctx, rows, nil)
}
func (w *Writer) qmutation(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	method := http.MethodPost
	if path == "points" {
		method = http.MethodPut
	}
	var wire struct {
		Result *struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	if err := w.r.request(ctx, method, "/collections/"+w.r.o.Index+"/"+path+"?wait=true", "application/json", data, &wire); err != nil {
		return err
	}
	if wire.Result == nil || wire.Result.Status != "completed" {
		return errors.New("retrieval: Qdrant mutation not completed")
	}
	return nil
}
func (w *Writer) removeChunks(ctx context.Context, source string) error {
	if w.r.provider == "qdrant" {
		return w.qmutation(ctx, "points/delete", map[string]any{"filter": map[string]any{"must": []any{map[string]any{"key": "namespace", "match": map[string]any{"value": w.r.o.Namespace}}, map[string]any{"key": "chunk.source", "match": map[string]any{"value": source}}}}})
	}
	var wire struct {
		TimedOut  bool              `json:"timed_out"`
		Conflicts int               `json:"version_conflicts"`
		Failures  []json.RawMessage `json:"failures"`
		Deleted   *int              `json:"deleted"`
	}
	if err := w.r.post(ctx, "/"+w.r.o.Index+"/_delete_by_query?refresh=true", map[string]any{"query": map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"term": map[string]any{"namespace": w.r.o.Namespace}}, map[string]any{"term": map[string]any{"chunk.source": source}}}}}}, &wire); err != nil {
		return err
	}
	if wire.TimedOut || wire.Conflicts > 0 || len(wire.Failures) > 0 || wire.Deleted == nil {
		return errors.New("retrieval: incomplete Elasticsearch deletion")
	}
	return nil
}
func (w *Writer) esBulk(ctx context.Context, rows []map[string]any, _ any) error {
	var body strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		body.Write(data)
		body.WriteByte('\n')
	}
	var wire struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := w.r.request(ctx, http.MethodPost, "/"+w.r.o.Index+"/_bulk?refresh=true", "application/x-ndjson", []byte(body.String()), &wire); err != nil {
		return err
	}
	expected := 0
	for _, row := range rows {
		if _, ok := row["index"]; ok {
			expected++
		}
		if _, ok := row["delete"]; ok {
			expected++
		}
	}
	if wire.Errors || len(wire.Items) != expected {
		return errors.New("retrieval: incomplete Elasticsearch bulk")
	}
	for _, item := range wire.Items {
		if len(item) != 1 {
			return errors.New("retrieval: invalid bulk item")
		}
		for operation, result := range item {
			if result.Status < 200 || result.Status >= 300 {
				if operation == "delete" && result.Status == 404 {
					continue
				}
				return fmt.Errorf("retrieval: Elasticsearch bulk item HTTP %d", result.Status)
			}
			if len(result.Error) > 0 && string(result.Error) != "null" {
				return errors.New("retrieval: Elasticsearch bulk item error")
			}
		}
	}
	return nil
}

// Sources lists the complete manifest directory, ordered by source. Missing or
// repeated pagination state and size limits fail the call instead of truncating.
func (w *Writer) Sources(ctx context.Context) ([]rag.SourceInfo, error) {
	if err := w.lock(ctx); err != nil {
		return nil, err
	}
	defer w.unlock()
	var infos []rag.SourceInfo
	var offset any
	seenOffsets := map[string]bool{}
	seenSources := map[string]bool{}
	for {
		var records []map[string]json.RawMessage
		var next any
		if w.r.provider == "qdrant" {
			body := map[string]any{"limit": 100, "with_payload": true, "with_vector": false, "filter": map[string]any{"must": []any{map[string]any{"key": "namespace", "match": map[string]any{"value": w.r.o.Namespace}}, map[string]any{"key": "agentx_record", "match": map[string]any{"value": "manifest"}}}}}
			if offset != nil {
				body["offset"] = offset
			}
			var wire struct {
				Result *struct {
					Points []struct {
						Payload map[string]json.RawMessage `json:"payload"`
					} `json:"points"`
					Next any `json:"next_page_offset"`
				} `json:"result"`
			}
			if err := w.r.post(ctx, "/collections/"+w.r.o.Index+"/points/scroll", body, &wire); err != nil {
				return nil, err
			}
			if wire.Result == nil || wire.Result.Points == nil {
				return nil, errors.New("retrieval: incomplete Qdrant source directory")
			}
			for _, p := range wire.Result.Points {
				records = append(records, p.Payload)
			}
			next = wire.Result.Next
		} else {
			body := map[string]any{"size": 100, "query": map[string]any{"bool": map[string]any{"filter": []any{map[string]any{"term": map[string]any{"namespace": w.r.o.Namespace}}, map[string]any{"term": map[string]any{"agentx_record": "manifest"}}}}}, "sort": []any{map[string]any{"source_key": "asc"}}}
			if offset != nil {
				body["search_after"] = offset
			}
			var wire struct {
				TimedOut bool `json:"timed_out"`
				Shards   struct {
					Failed int `json:"failed"`
				} `json:"_shards"`
				Hits *struct {
					Hits []struct {
						Source map[string]json.RawMessage `json:"_source"`
						Sort   []any                      `json:"sort"`
					} `json:"hits"`
				} `json:"hits"`
			}
			if err := w.r.post(ctx, "/"+w.r.o.Index+"/_search", body, &wire); err != nil {
				return nil, err
			}
			if wire.TimedOut || wire.Shards.Failed > 0 || wire.Hits == nil || wire.Hits.Hits == nil {
				return nil, errors.New("retrieval: incomplete Elasticsearch source directory")
			}
			for _, h := range wire.Hits.Hits {
				records = append(records, h.Source)
				next = h.Sort
			}
			if len(records) < 100 {
				next = nil
			}
		}
		for _, record := range records {
			var ns, kind string
			var info rag.SourceInfo
			if json.Unmarshal(record["namespace"], &ns) != nil || json.Unmarshal(record["agentx_record"], &kind) != nil || json.Unmarshal(record["source_info"], &info) != nil || ns != w.r.o.Namespace || kind != "manifest" || info.Source == "" || seenSources[info.Source] {
				return nil, errors.New("retrieval: invalid source manifest")
			}
			seenSources[info.Source] = true
			infos = append(infos, info)
			if len(infos) > w.o.MaxSources {
				return nil, errors.New("retrieval: source directory exceeds limit")
			}
		}
		if next == nil {
			break
		}
		key, _ := json.Marshal(next)
		if len(records) == 0 || len(key) == 0 || string(key) == "[]" || seenOffsets[string(key)] {
			return nil, errors.New("retrieval: invalid source pagination")
		}
		seenOffsets[string(key)] = true
		offset = next
	}
	slices.SortFunc(infos, func(a, b rag.SourceInfo) int { return strings.Compare(a.Source, b.Source) })
	return infos, nil
}

var _ rag.SourceWriter = (*Writer)(nil)

// Close releases only this writer's internally constructed HTTP transport.
func (w *Writer) Close() error { return w.r.Close() }
