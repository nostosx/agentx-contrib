// Package s3 loads RAG documents from an S3 bucket using an injected AWS
// SDK v2 client. Credentials, endpoint selection and retries belong to the
// application. No ambient configuration is loaded by this package.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/nostosx/agentx/rag"
)

// Client is implemented by *s3.Client from the AWS SDK for Go v2.
// GetObject bodies must honor cancellation of the supplied context.
type Client interface {
	ListObjectsV2(context.Context, *awss3.ListObjectsV2Input, ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

// Loader lists and reads a prefix as one snapshot. Any listing, reading,
// limit or parsing error fails the whole load, so Index.Sync never interprets
// an incomplete load as deletion. A bucket is not an atomic snapshot: IfMatch
// guards individual reads, but changes across pages require a later Sync.
// A Loader may be used concurrently if its client and parsers are safe.
type Loader struct {
	Client Client
	Bucket string
	Prefix string
	// SourcePrefix is prepended to the canonical s3://bucket/key source.
	SourcePrefix string
	// Parsers maps lowercase extensions to parsers; nil uses rag.DefaultParsers.
	Parsers map[string]rag.Parser
	// MaxObjectSize defaults to rag.DefaultMaxFileSize (10 MiB).
	MaxObjectSize int64
	// MaxTotalSize defaults to 64 MiB of downloaded document bytes.
	MaxTotalSize int64
	// MaxObjects defaults to 1000 listed objects, including skipped formats.
	MaxObjects int
}

var _ rag.Loader = (*Loader)(nil)

// Load lists all pages under Prefix and parses supported document types.
// Unsupported extensions and directory markers are skipped. It neither
// creates buckets nor writes objects, and closes each downloaded body.
func (l *Loader) Load(ctx context.Context) ([]*rag.Document, error) {
	if l.Client == nil || l.Bucket == "" {
		return nil, errors.New("s3: loader requires a client and bucket")
	}
	maxObject, maxTotal, maxObjects := l.MaxObjectSize, l.MaxTotalSize, l.MaxObjects
	if maxObject <= 0 {
		maxObject = rag.DefaultMaxFileSize
	}
	if maxTotal <= 0 {
		maxTotal = 64 << 20
	}
	if maxObjects <= 0 {
		maxObjects = 1000
	}
	parsers := l.Parsers
	if parsers == nil {
		parsers = rag.DefaultParsers()
	}
	var docs []*rag.Document
	var continuation *string
	seenPages, seenKeys := map[string]bool{}, map[string]bool{}
	var total int64
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := l.Client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: &l.Bucket, Prefix: &l.Prefix, ContinuationToken: continuation})
		if err != nil {
			return nil, fmt.Errorf("s3: list objects: %w", err)
		}
		if page == nil {
			return nil, errors.New("s3: empty listing response")
		}
		for _, object := range page.Contents {
			count++
			if count > maxObjects {
				return nil, errors.New("s3: listing exceeds MaxObjects")
			}
			if object.Key == nil || !strings.HasPrefix(*object.Key, l.Prefix) {
				return nil, errors.New("s3: invalid object key in listing")
			}
			key := *object.Key
			if seenKeys[key] {
				return nil, errors.New("s3: duplicate object in listing")
			}
			seenKeys[key] = true
			if strings.HasSuffix(key, "/") {
				continue
			}
			parser := parsers[strings.ToLower(path.Ext(key))]
			if parser == nil {
				continue
			}
			if object.Size != nil && *object.Size > maxObject {
				return nil, errors.New("s3: object exceeds MaxObjectSize")
			}
			limit := min(maxObject, maxTotal-total)
			if limit < 0 {
				return nil, errors.New("s3: download exceeds MaxTotalSize")
			}
			data, etag, err := l.read(ctx, key, object.ETag, limit)
			if err != nil {
				return nil, err
			}
			total += int64(len(data))
			source := l.SourcePrefix + (&url.URL{Scheme: "s3", Host: l.Bucket, Path: "/" + key}).String()
			parsed, err := parser.Parse(ctx, source, data)
			if err != nil {
				return nil, fmt.Errorf("s3: parse object: %w", err)
			}
			for _, doc := range parsed {
				if doc == nil {
					return nil, errors.New("s3: parser returned a nil document")
				}
				if doc.Metadata == nil {
					doc.Metadata = map[string]any{}
				}
				doc.Metadata["s3_bucket"], doc.Metadata["s3_key"], doc.Metadata["s3_etag"] = l.Bucket, key, etag
			}
			docs = append(docs, parsed...)
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			break
		}
		if page.NextContinuationToken == nil || *page.NextContinuationToken == "" || seenPages[*page.NextContinuationToken] {
			return nil, errors.New("s3: invalid pagination token")
		}
		seenPages[*page.NextContinuationToken] = true
		continuation = page.NextContinuationToken
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

func (l *Loader) read(ctx context.Context, key string, etag *string, limit int64) ([]byte, string, error) {
	obj, err := l.Client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &l.Bucket, Key: &key, IfMatch: etag})
	if err != nil {
		return nil, "", fmt.Errorf("s3: get object: %w", err)
	}
	if obj == nil || obj.Body == nil {
		return nil, "", errors.New("s3: empty object response")
	}
	defer obj.Body.Close()
	if obj.ContentLength != nil && *obj.ContentLength > limit {
		return nil, "", errors.New("s3: object exceeds download limit")
	}
	// Avoid limit+1 overflow for a caller-provided MaxInt64 limit.
	if limit > int64(^uint64(0)>>1)-1 {
		limit--
	}
	data, err := io.ReadAll(io.LimitReader(obj.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("s3: read object: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if int64(len(data)) > limit {
		return nil, "", errors.New("s3: object exceeds download limit")
	}
	var tag string
	if obj.ETag != nil {
		tag = *obj.ETag
	}
	return data, tag, nil
}
