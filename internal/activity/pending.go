package activity

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/robertjndw/oakestra-mcp/internal/actions"
)

var _ actions.Store = (*Store)(nil)

// SavePending stores a prepared action.
func (s *Store) SavePending(ctx context.Context, a actions.Pending) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_actions (id, user, organization, kind, params_json, plan_hash, created, expires)
		 VALUES (?,?,?,?,?,?,?,?)`,
		a.ID, a.User, a.Organization, string(a.Kind), a.ParamsJSON, a.PlanHash, a.Created.Unix(), a.Expires.Unix())
	return err
}

// LoadPending returns the action with the given ID, or actions.ErrNotFound.
func (s *Store) LoadPending(ctx context.Context, id string) (actions.Pending, error) {
	var (
		a                actions.Pending
		kind             string
		created, expires int64
		consumed         sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user, organization, kind, params_json, plan_hash, created, expires, consumed_at
		 FROM pending_actions WHERE id = ?`, id).
		Scan(&a.ID, &a.User, &a.Organization, &kind, &a.ParamsJSON, &a.PlanHash, &created, &expires, &consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return actions.Pending{}, actions.ErrNotFound
	}
	if err != nil {
		return actions.Pending{}, err
	}
	a.Kind = actions.Kind(kind)
	a.Created = time.Unix(created, 0).UTC()
	a.Expires = time.Unix(expires, 0).UTC()
	if consumed.Valid {
		t := time.Unix(consumed.Int64, 0).UTC()
		a.ConsumedAt = &t
	}
	return a, nil
}

// ConsumePending marks the action as used and reports whether this call
// was the one that did it. The conditional UPDATE is what makes a token run
// at most once: SQLite serialises writers, so of two racing confirms only
// one sees a row with consumed_at still NULL.
//
// The params are cleared at the same time. A deploy's SLA can carry
// environment variables, and once the action has run nothing needs them.
func (s *Store) ConsumePending(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE pending_actions SET consumed_at = ?, params_json = '{}' WHERE id = ? AND consumed_at IS NULL`,
		at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PrunePending deletes actions that expired before the cutoff, confirmed
// or not. Their tokens can no longer be used, and the activity log keeps
// the record of what happened.
func (s *Store) PrunePending(ctx context.Context, expiredBefore time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM pending_actions WHERE expires < ?`, expiredBefore.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
