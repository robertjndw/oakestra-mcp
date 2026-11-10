// Package activity keeps an audit log of MCP tool calls in SQLite.
package activity

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, the image builds with CGO off
)

// tsFormat is fixed-width so that string comparison in SQL orders like time.
const tsFormat = "2006-01-02T15:04:05Z"

// Entry is one stored tool call.
type Entry struct {
	ID           int64
	TS           time.Time
	User         string
	Organization string
	ClientName   string
	Tool         string
	Kind         string
	ArgsJSON     string
	Outcome      string
	Error        string
	DurationMS   int64
	ActionID     string
}

// Filter narrows List. Zero values mean "no constraint".
type Filter struct {
	User    string
	Tool    string
	Outcome string
	// Before restricts to ids strictly below it, for paging.
	Before int64
	Limit  int
}

// Store is the SQLite backed activity log.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it.
func Open(path string) (*Store, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "synchronous(NORMAL)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// One writer goroutine plus occasional readers; a small pool avoids
	// piling up connections on a file that serialises writes anyway.
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("activity: migrate: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE calls (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ts TEXT NOT NULL,
		user TEXT NOT NULL,
		organization TEXT NOT NULL,
		client_name TEXT NOT NULL,
		tool TEXT NOT NULL,
		kind TEXT NOT NULL,
		args_json TEXT NOT NULL,
		outcome TEXT NOT NULL,
		error TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		action_id TEXT NOT NULL
	);
	CREATE INDEX calls_user_id ON calls (user, id);
	CREATE INDEX calls_tool_id ON calls (tool, id);
	CREATE INDEX calls_ts ON calls (ts);`,
	// Times are unix seconds: expiry checks compare numbers, and nothing
	// here is shown to people directly.
	`CREATE TABLE pending_actions (
		id TEXT PRIMARY KEY,
		user TEXT NOT NULL,
		organization TEXT NOT NULL,
		kind TEXT NOT NULL,
		params_json TEXT NOT NULL,
		plan_hash TEXT NOT NULL,
		created INTEGER NOT NULL,
		expires INTEGER NOT NULL,
		consumed_at INTEGER
	);
	CREATE INDEX pending_actions_expires ON pending_actions (expires);`,
}

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			_ = tx.Rollback()
			return err
		}
		// PRAGMA does not take bound parameters.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Insert stores one entry and returns its id.
func (s *Store) Insert(ctx context.Context, e Entry) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO calls (ts, user, organization, client_name, tool, kind, args_json, outcome, error, duration_ms, action_id)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		e.TS.UTC().Format(tsFormat), e.User, e.Organization, e.ClientName, e.Tool, e.Kind,
		e.ArgsJSON, e.Outcome, e.Error, e.DurationMS, e.ActionID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// List returns entries newest first. Paging callers ask for one row more than
// they show, to learn whether another page exists.
func (s *Store) List(ctx context.Context, f Filter) ([]Entry, error) {
	q := `SELECT id, ts, user, organization, client_name, tool, kind, args_json, outcome, error, duration_ms, action_id FROM calls WHERE 1=1`
	var args []any
	if f.User != "" {
		q += ` AND user = ?`
		args = append(args, f.User)
	}
	if f.Tool != "" {
		q += ` AND tool = ?`
		args = append(args, f.Tool)
	}
	if f.Outcome != "" {
		q += ` AND outcome = ?`
		args = append(args, f.Outcome)
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	q += ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.User, &e.Organization, &e.ClientName, &e.Tool, &e.Kind,
			&e.ArgsJSON, &e.Outcome, &e.Error, &e.DurationMS, &e.ActionID); err != nil {
			return nil, err
		}
		e.TS, _ = time.Parse(tsFormat, ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than the cutoff and returns how many went.
func (s *Store) Prune(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM calls WHERE ts < ?`, olderThan.UTC().Format(tsFormat))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
