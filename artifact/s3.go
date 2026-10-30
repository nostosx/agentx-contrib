// Package artifact adapts servex-contrib object storage to agentx artifacts.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aws/smithy-go"

	"github.com/nostosx/servex-contrib/storage/s3"

	core "github.com/nostosx/agentx/artifact"
)

// S3 stores one JSON envelope per artifact. Payload and metadata are replaced
// atomically in a single object; its URL is an envelope, not a media download.
// Credentials, bucket, key prefix, SDK retries and transport belong to servex's
// Store, configured and owned by the application.
type S3 struct {
	store    *s3.Store
	maxBytes int64
}

var _ core.Store = (*S3)(nil)

// S3Options controls payload limits. MaxBytes defaults to 64 MiB; the maximum
// supported bound is 256 MiB. This backend buffers bounded JSON envelopes.
type S3Options struct{ MaxBytes int64 }

// NewS3 adapts an existing servex-contrib S3 Store without creating an SDK client.
func NewS3(store *s3.Store, opts S3Options) (*S3, error) {
	if store == nil {
		return nil, errors.New("artifact: S3 store is required")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 64 << 20
	}
	if opts.MaxBytes < 1 || opts.MaxBytes > 256<<20 {
		return nil, errors.New("artifact: MaxBytes must be between 1 and 256 MiB")
	}
	return &S3{store: store, maxBytes: opts.MaxBytes}, nil
}

func objectKey(key core.Key) string {
	b, _ := json.Marshal(key)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]) + ".json"
}

// Put replaces the artifact in one S3 write. It does not mutate value.
func (s *S3) Put(ctx context.Context, value *core.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if int64(len(value.Data)) > s.maxBytes {
		return errors.New("artifact: payload exceeds MaxBytes")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("artifact: encode: %w", err)
	}
	if int64(len(b)) > s.envelopeLimit() {
		return errors.New("artifact: metadata exceeds envelope limit")
	}
	_, err = s.store.Put(ctx, objectKey(value.Key), bytes.NewReader(b), int64(len(b)), "application/json")
	return err
}

func (s *S3) envelopeLimit() int64 { return (s.maxBytes+2)/3*4 + (1 << 20) }

// Get reads a bounded envelope and verifies its scoped identity.
func (s *S3) Get(ctx context.Context, key core.Key) (*core.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	obj, err := s.store.Get(ctx, objectKey(key))
	if err != nil {
		if api, ok := errors.AsType[smithy.APIError](err); ok && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			return nil, core.ErrNotFound
		}
		return nil, err
	}
	if obj == nil || obj.Body == nil {
		return nil, errors.New("artifact: empty object response")
	}
	defer obj.Body.Close()
	b, err := io.ReadAll(io.LimitReader(obj.Body, s.envelopeLimit()+1))
	if err != nil {
		return nil, fmt.Errorf("artifact: read: %w", err)
	}
	if int64(len(b)) > s.envelopeLimit() {
		return nil, errors.New("artifact: envelope exceeds limit")
	}
	var a core.Artifact
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("artifact: decode: %w", err)
	}
	if a.Key != key {
		return nil, errors.New("artifact: stored key mismatch")
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	if int64(len(a.Data)) > s.maxBytes {
		return nil, errors.New("artifact: payload exceeds MaxBytes")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &a, nil
}

// Delete removes an artifact. Missing keys are already deleted.
func (s *S3) Delete(ctx context.Context, key core.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := key.Validate(); err != nil {
		return err
	}
	return s.store.Delete(ctx, objectKey(key))
}
