package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nostosx/agentx/session"
)

// sessionIDPattern matches the session IDs of session.Store.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}$`)

// maxOverwriteAttempts bounds the retries of a Save that overwrites a
// session other writers keep changing.
const maxOverwriteAttempts = 100

// SessionStore is a session.Store in a SQL table. It accepts the session
// IDs session.Store documents and detects concurrent modification like
// session.MemoryStore, atomically across processes: a session loaded or
// saved by a store is updated only at that revision. A missing row may be
// recreated; a conflicting stored revision makes Save return
// session.ErrConflict.
type SessionStore struct {
	c config
}

var _ session.Store = (*SessionStore)(nil)

// Sessions returns a session store in db.
func Sessions(db DB, opts ...Option) *SessionStore {
	return &SessionStore{c: newConfig(db, opts)}
}

// Migrate creates the sessions table if it does not exist.
func (s *SessionStore) Migrate(ctx context.Context) error {
	c := s.c
	return c.migrate(ctx, c.createTable("sessions", []string{
		"id " + c.keyType(128) + " NOT NULL",
		"revision BIGINT NOT NULL",
		"messages BIGINT NOT NULL",
		"created_at " + timeType + " NOT NULL",
		"updated_at " + timeType + " NOT NULL",
		"data " + c.textType() + " NOT NULL",
	}, "id"))
}

func checkSessionID(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}

// Get implements session.Store.
func (s *SessionStore) Get(ctx context.Context, id string) (session.Session, error) {
	if s.c.err != nil {
		return nil, s.c.err
	}
	if err := checkSessionID(id); err != nil {
		return nil, err
	}
	var (
		rev  int64
		data []byte
	)
	err := s.c.queryRow(ctx, "SELECT revision, data FROM "+s.c.table("sessions")+" WHERE id = ?", id).Scan(&rev, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var snap session.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode session %s: %w", id, err)
	}
	snap.ID, snap.Revision = id, rev
	return session.Loaded(&snap), nil
}

// sessionRow is a session as written.
type sessionRow struct {
	id                 string
	messages           int
	created, updated   string
	data               string
	revision, expected int64
}

// Save implements session.Store.
func (s *SessionStore) Save(ctx context.Context, sess session.Session) error {
	if s.c.err != nil {
		return s.c.err
	}
	if err := checkSessionID(sess.ID()); err != nil {
		return err
	}
	snap := session.TakeSnapshot(sess)
	// The revision column is authoritative.
	snap.Revision = 0
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	row := &sessionRow{id: snap.ID, messages: len(snap.Messages), created: formatTime(snap.CreatedAt), updated: formatTime(snap.UpdatedAt), data: string(data)}
	var rev int64
	if expected, tracked := session.ExpectedRevision(sess); tracked {
		rev, err = s.saveTracked(ctx, row, expected)
	} else {
		rev, err = s.overwrite(ctx, row)
	}
	if err != nil {
		return err
	}
	session.MarkSaved(sess, rev)
	return nil
}

// saveTracked saves a session that expects to find the stored revision
// expected, or no stored session.
func (s *SessionStore) saveTracked(ctx context.Context, row *sessionRow, expected int64) (int64, error) {
	if expected > 0 {
		row.revision, row.expected = expected+1, expected
		if ok, err := s.update(ctx, row); err != nil || ok {
			return row.revision, err
		}
	}
	// The session is stored at another revision, a conflict, or not at
	// all. A session deleted since it was loaded is stored again after
	// its revision, so other copies loaded before the deletion conflict.
	row.revision = max(expected, 0) + 1
	inserted, err := s.insert(ctx, row)
	if err != nil {
		return 0, err
	}
	if !inserted {
		return 0, fmt.Errorf("session %s: %w", row.id, session.ErrConflict)
	}
	return row.revision, nil
}

// overwrite saves a session over whatever is stored.
func (s *SessionStore) overwrite(ctx context.Context, row *sessionRow) (int64, error) {
	for range maxOverwriteAttempts {
		var current int64
		err := s.c.queryRow(ctx, "SELECT revision FROM "+s.c.table("sessions")+" WHERE id = ?", row.id).Scan(&current)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			row.revision = 1
			inserted, err := s.insert(ctx, row)
			if err != nil || inserted {
				return row.revision, err
			}
		case err != nil:
			return 0, err
		default:
			row.revision, row.expected = current+1, current
			ok, err := s.update(ctx, row)
			if err != nil || ok {
				return row.revision, err
			}
		}
		// Another writer saved the session in between; try again.
	}
	return 0, fmt.Errorf("session %s: gave up after %d concurrent modifications: %w", row.id, maxOverwriteAttempts, session.ErrConflict)
}

// update writes row if the stored session is at row.expected.
func (s *SessionStore) update(ctx context.Context, row *sessionRow) (bool, error) {
	res, err := s.c.exec(ctx, "UPDATE "+s.c.table("sessions")+
		" SET revision = ?, messages = ?, created_at = ?, updated_at = ?, data = ? WHERE id = ? AND revision = ?",
		row.revision, row.messages, row.created, row.updated, row.data, row.id, row.expected)
	if err != nil {
		return false, err
	}
	n, err := affected(res)
	return n == 1, err
}

// insert writes row unless a session with its ID is stored.
func (s *SessionStore) insert(ctx context.Context, row *sessionRow) (bool, error) {
	stmt := "INSERT INTO " + s.c.table("sessions") + " (id, revision, messages, created_at, updated_at, data) VALUES (?, ?, ?, ?, ?, ?)"
	args := []any{row.id, row.revision, row.messages, row.created, row.updated, row.data}
	if s.c.dialect == MySQL {
		// The alternatives on MySQL, INSERT IGNORE and a no-op ON
		// DUPLICATE KEY UPDATE, downgrade other errors to warnings or
		// report ambiguous row counts, so a failed insert is checked.
		if _, err := s.c.exec(ctx, stmt, args...); err != nil {
			var one int
			if lookup := s.c.queryRow(ctx, "SELECT 1 FROM "+s.c.table("sessions")+" WHERE id = ?", row.id).Scan(&one); lookup == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	res, err := s.c.exec(ctx, stmt+" ON CONFLICT (id) DO NOTHING", args...)
	if err != nil {
		return false, err
	}
	n, err := affected(res)
	return n == 1, err
}

// Delete implements session.Store. Deleting a missing session succeeds.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	if s.c.err != nil {
		return s.c.err
	}
	if err := checkSessionID(id); err != nil {
		return err
	}
	_, err := s.c.exec(ctx, "DELETE FROM "+s.c.table("sessions")+" WHERE id = ?", id)
	return err
}

// List implements session.Store. It reads only the summary columns.
func (s *SessionStore) List(ctx context.Context) ([]session.Info, error) {
	if s.c.err != nil {
		return nil, s.c.err
	}
	rows, err := s.c.query(ctx, "SELECT id, messages, created_at, updated_at FROM "+s.c.table("sessions"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session.Info
	for rows.Next() {
		var (
			info             session.Info
			created, updated string
		)
		if err := rows.Scan(&info.ID, &info.Messages, &created, &updated); err != nil {
			return nil, err
		}
		if info.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("session %s: %w", info.ID, err)
		}
		if info.UpdatedAt, err = parseTime(updated); err != nil {
			return nil, fmt.Errorf("session %s: %w", info.ID, err)
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sorting here rather than in SQL keeps the order of the other stores
	// independent of the database's collation.
	slices.SortFunc(out, func(a, b session.Info) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}
