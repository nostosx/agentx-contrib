package filestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
	"github.com/nostosx/agentx/artifact"
)

// ArtifactStore persists artifacts in private JSON files, using the same atomic
// replacement and cross-process sidecar locks as the other file stores.
type ArtifactStore struct {
	files *jsonfile.JSONDir[artifact.Artifact]
}

var _ artifact.Store = (*ArtifactStore)(nil)

// Artifacts opens dir for artifact storage. Keys are hashed, so scopes and IDs
// remain case-sensitive and cannot escape dir. The application owns disk quotas.
func Artifacts(dir string) (*ArtifactStore, error) {
	files, err := jsonfile.NewJSONDir[artifact.Artifact](dir, "artifact", regexp.MustCompile(`^[a-f0-9]{64}$`), artifact.ErrNotFound,
		func(a *artifact.Artifact) string { return artifactID(a.Key) })
	if err != nil {
		return nil, err
	}
	return &ArtifactStore{files: files}, nil
}

func artifactID(key artifact.Key) string {
	b, _ := json.Marshal(key)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Put atomically replaces the value at its scoped key without changing value.
func (s *ArtifactStore) Put(ctx context.Context, value *artifact.Artifact) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := value.Validate(); err != nil {
		return err
	}
	return s.files.Save(ctx, artifactID(value.Key), value)
}

// Get returns an independent artifact value, reloading it from disk.
func (s *ArtifactStore) Get(ctx context.Context, key artifact.Key) (*artifact.Artifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := key.Validate(); err != nil {
		return nil, err
	}
	a, err := s.files.Load(ctx, artifactID(key))
	if err != nil {
		return nil, err
	}
	if a.Key != key {
		return nil, fmt.Errorf("filestore: artifact key mismatch")
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// Delete removes the artifact; an absent key is not an error.
func (s *ArtifactStore) Delete(ctx context.Context, key artifact.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := key.Validate(); err != nil {
		return err
	}
	return s.files.Delete(ctx, artifactID(key))
}
