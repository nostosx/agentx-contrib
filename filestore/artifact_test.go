package filestore

import (
	"testing"

	"github.com/nostosx/agentx/artifact"
	"github.com/nostosx/agentx/artifact/artifacttest"
)

func TestArtifacts(t *testing.T) {
	artifacttest.Run(t, func(t *testing.T) artifact.Store {
		store, err := Artifacts(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return store
	})
}

func TestArtifactsReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Artifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &artifact.Artifact{Scope: "user", ID: "generated", MediaType: "image/png", Data: []byte("image")}
	if err := s.Put(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	s, err = Artifacts(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), a.Key)
	if err != nil || string(got.Data) != "image" {
		t.Fatalf("reopen = %+v, %v", got, err)
	}
}
