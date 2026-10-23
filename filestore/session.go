package filestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/nostosx/agentx-contrib/filestore/internal/jsonfile"
	"github.com/nostosx/agentx/session"
)

// sessionIDPattern matches the session IDs of session.Store. A leading dot
// is rejected because List skips hidden files.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}$`)

// SessionStore stores each session as a JSON file in a directory.
//
// Save detects concurrent modification (see session.ErrConflict) by
// comparing revisions. To read the stored revision, each Save loads and
// decodes the session's current file before writing the new one, so a Save
// costs a full read as well as a write. Saves through one SessionStore are
// serialized; separate instances, other processes and concurrent Delete
// calls are not coordinated with Save's revision check. Use external locking
// when those operations can overlap.
type SessionStore struct {
	mu  sync.Mutex // serializes Save
	dir *jsonfile.JSONDir[session.Snapshot]
}

var _ session.Store = (*SessionStore)(nil)

// Sessions returns a session store rooted at dir, creating it if needed.
func Sessions(dir string) (*SessionStore, error) {
	d, err := jsonfile.NewJSONDir(dir, "session", sessionIDPattern, session.ErrNotFound, func(snap *session.Snapshot) string { return snap.ID })
	if err != nil {
		return nil, err
	}
	return &SessionStore{dir: d}, nil
}

// Get implements session.Store.
func (s *SessionStore) Get(_ context.Context, id string) (session.Session, error) {
	snap, err := s.dir.Load(id)
	if err != nil {
		return nil, err
	}
	return session.Loaded(snap), nil
}

// Save implements session.Store using temporary-file replacement. See
// SessionStore for revision-check concurrency limits and the package
// documentation for platform guarantees.
func (s *SessionStore) Save(_ context.Context, sess session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := session.TakeSnapshot(sess)
	var current int64
	old, err := s.dir.Load(snap.ID)
	exists := err == nil
	switch {
	case err == nil:
		current = old.Revision
	case errors.Is(err, session.ErrNotFound) || isDecodeError(err):
		// Missing or undecodable contents have no usable stored revision.
	default:
		return err
	}
	if snap.Revision, err = nextRevision(sess, current, exists); err != nil {
		return err
	}
	if err := s.dir.Save(snap.ID, snap); err != nil {
		return err
	}
	session.MarkSaved(sess, snap.Revision)
	return nil
}

// nextRevision decides the revision of a save of sess over a stored
// revision current (exists reports whether one is stored). It returns
// session.ErrConflict when sess was loaded from or saved to a store and the
// stored session changed since. A tracked session saved after the stored
// one was deleted continues from its own revision, so copies loaded before
// the deletion conflict.
func nextRevision(sess session.Session, current int64, exists bool) (int64, error) {
	rev, tracked := session.ExpectedRevision(sess)
	switch {
	case tracked && exists && rev != current:
		return 0, fmt.Errorf("session %s: %w", sess.ID(), session.ErrConflict)
	case tracked && !exists && rev > current:
		return rev + 1, nil
	}
	return current + 1, nil
}

// Delete implements session.Store.
func (s *SessionStore) Delete(_ context.Context, id string) error { return s.dir.Delete(id) }

// List implements session.Store. Unreadable files are skipped.
func (s *SessionStore) List(context.Context) ([]session.Info, error) {
	ids, err := s.dir.IDs()
	if err != nil {
		return nil, err
	}
	var out []session.Info
	for _, id := range ids {
		snap, err := s.dir.Load(id)
		if err != nil {
			continue
		}
		out = append(out, session.Info{ID: snap.ID, Messages: len(snap.Messages), CreatedAt: snap.CreatedAt, UpdatedAt: snap.UpdatedAt})
	}
	slices.SortFunc(out, func(a, b session.Info) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func isDecodeError(err error) bool {
	_, syntax := errors.AsType[*json.SyntaxError](err)
	_, typ := errors.AsType[*json.UnmarshalTypeError](err)
	return syntax || typ || errors.Is(err, io.ErrUnexpectedEOF)
}
