package artifact_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	stores3 "github.com/nostosx/servex-contrib/storage/s3"

	adapter "github.com/nostosx/agentx-contrib/artifact"
	"github.com/nostosx/agentx/artifact"
	"github.com/nostosx/agentx/artifact/artifacttest"
)

func fixture(t *testing.T, maxBytes int64) *adapter.S3 {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			objects[r.URL.Path] = b
		case http.MethodGet:
			b, ok := objects[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, string(b))
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	client := sdk.New(sdk.Options{Region: "us-east-1", BaseEndpoint: aws.String(srv.URL), UsePathStyle: true, HTTPClient: srv.Client(), Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
	})})
	store, err := stores3.New(client, "artifacts", "test/")
	if err != nil {
		t.Fatal(err)
	}
	s, err := adapter.NewS3(store, adapter.S3Options{MaxBytes: maxBytes})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestS3(t *testing.T) {
	artifacttest.Run(t, func(t *testing.T) artifact.Store { return fixture(t, 1024) })
}

func TestS3Limits(t *testing.T) {
	s := fixture(t, 3)
	err := s.Put(t.Context(), &artifact.Artifact{Scope: "s", ID: "i", MediaType: "text/plain", Data: []byte("four")})
	if err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := s.Get(t.Context(), artifact.Key{Scope: "s", ID: "i"}); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("unexpected stored value: %v", err)
	}
	if _, err := adapter.NewS3(nil, adapter.S3Options{}); err == nil {
		t.Fatal("nil backend accepted")
	}
}
