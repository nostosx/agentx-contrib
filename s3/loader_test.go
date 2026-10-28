package s3_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	loader "github.com/nostosx/agentx-contrib/s3"
)

type client struct {
	list func(context.Context, *awss3.ListObjectsV2Input) (*awss3.ListObjectsV2Output, error)
	get  func(context.Context, *awss3.GetObjectInput) (*awss3.GetObjectOutput, error)
}

func (c client) ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	return c.list(ctx, in)
}
func (c client) GetObject(ctx context.Context, in *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return c.get(ctx, in)
}

type body struct {
	io.Reader
	closed bool
}

func (b *body) Close() error { b.closed = true; return nil }

func TestLoadPages(t *testing.T) {
	t.Parallel()
	pages := 0
	b := &body{Reader: strings.NewReader("hello")}
	c := client{
		list: func(ctx context.Context, in *awss3.ListObjectsV2Input) (*awss3.ListObjectsV2Output, error) {
			if ctx != t.Context() || *in.Bucket != "docs" || *in.Prefix != "kb/" {
				t.Fatal("request configuration lost")
			}
			pages++
			if pages == 1 {
				return &awss3.ListObjectsV2Output{IsTruncated: new(true), NextContinuationToken: new("next"), Contents: []types.Object{{Key: new("kb/file.txt"), ETag: new("etag")}}}, nil
			}
			if in.ContinuationToken == nil || *in.ContinuationToken != "next" {
				t.Fatal("pagination token lost")
			}
			return &awss3.ListObjectsV2Output{Contents: []types.Object{{Key: new("kb/skip.bin")}}}, nil
		},
		get: func(_ context.Context, in *awss3.GetObjectInput) (*awss3.GetObjectOutput, error) {
			if in.IfMatch == nil || *in.IfMatch != "etag" {
				t.Fatal("conditional read lost")
			}
			return &awss3.GetObjectOutput{Body: b, ETag: new("etag")}, nil
		},
	}
	l := &loader.Loader{Client: c, Bucket: "docs", Prefix: "kb/"}
	docs, err := l.Load(t.Context())
	if err != nil || len(docs) != 1 || docs[0].Content != "hello" || docs[0].Source != "s3://docs/kb/file.txt" || docs[0].Metadata["s3_etag"] != "etag" || !b.closed || pages != 2 {
		t.Fatalf("docs=%v closed=%v pages=%d err=%v", docs, b.closed, pages, err)
	}
}

func TestLoadFailsAsWhole(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"object limit", "total limit", "object count", "repeated page", "read error", "listing error"} {
		t.Run(name, func(t *testing.T) {
			b := &body{Reader: strings.NewReader("hello")}
			c := client{
				list: func(context.Context, *awss3.ListObjectsV2Input) (*awss3.ListObjectsV2Output, error) {
					if name == "listing error" {
						return nil, errors.New("listing failed")
					}
					page := &awss3.ListObjectsV2Output{Contents: []types.Object{{Key: new("a.txt")}, {Key: new("b.txt")}}}
					if name == "repeated page" {
						page.IsTruncated, page.NextContinuationToken = new(true), new("same")
					}
					return page, nil
				},
				get: func(_ context.Context, in *awss3.GetObjectInput) (*awss3.GetObjectOutput, error) {
					if name == "read error" && *in.Key == "b.txt" {
						return nil, errors.New("read failed")
					}
					return &awss3.GetObjectOutput{Body: b}, nil
				},
			}
			l := &loader.Loader{Client: c, Bucket: "docs"}
			switch name {
			case "object limit":
				l.MaxObjectSize = 2
			case "total limit":
				l.MaxTotalSize = 2
			case "object count":
				l.MaxObjects = 1
			}
			if docs, err := l.Load(t.Context()); err == nil || docs != nil {
				t.Fatalf("partial load accepted: %v %v", docs, err)
			}
			if name != "listing error" && !b.closed {
				t.Fatal("response body not closed")
			}
		})
	}
}

func TestSDKHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated><Contents><Key>hello.txt</Key><ETag>"abc"</ETag><Size>5</Size></Contents></ListBucketResult>`)
			return
		}
		if r.URL.Path != "/docs/hello.txt" || r.Header.Get("If-Match") != `"abc"` {
			t.Error("unexpected object request")
		}
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()
	c := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: &srv.URL, UsePathStyle: true, Credentials: aws.AnonymousCredentials{}, HTTPClient: srv.Client()})
	docs, err := (&loader.Loader{Client: c, Bucket: "docs"}).Load(t.Context())
	if err != nil || len(docs) != 1 || docs[0].Content != "hello" {
		t.Fatalf("SDK roundtrip: %v %v", docs, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (&loader.Loader{Client: c, Bucket: "docs"}).Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
