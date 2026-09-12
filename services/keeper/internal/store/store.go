package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("keeper store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("keeper store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type Subscription struct {
	UserID   string
	PrivyDID string
	BasketID string
	Chain    string
}

type Position struct {
	Asset     string
	VenueID   string
	Chain     string
	AmountUSD float64
	EntryAPY  float64
}

func (s *Store) ActiveSubscriptions(ctx context.Context) ([]Subscription, error) {
	const q = `
		SELECT s.user_id::text, u.privy_did, s.basket_id::text, b.chain
		FROM subscriptions s
		JOIN users   u ON u.id = s.user_id
		JOIN baskets b ON b.id = s.basket_id
		WHERE s.status = 'active'
		  AND u.delegated
		  AND COALESCE(u.privy_wallet_id, '') <> ''
		ORDER BY s.user_id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("keeper store: subscriptions: %w", err)
	}
	defer rows.Close()

	var out []Subscription
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.UserID, &sub.PrivyDID, &sub.BasketID, &sub.Chain); err != nil {
			return nil, err
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}

func (s *Store) Positions(ctx context.Context, userID, basketID string) ([]Position, error) {
	const q = `
		SELECT asset, venue_id, chain, amount_usd, entry_apy
		FROM positions
		WHERE user_id = $1 AND basket_id = $2 AND amount_usd > 0`
	rows, err := s.pool.Query(ctx, q, userID, basketID)
	if err != nil {
		return nil, fmt.Errorf("keeper store: positions: %w", err)
	}
	defer rows.Close()

	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.Asset, &p.VenueID, &p.Chain, &p.AmountUSD, &p.EntryAPY); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type History struct {
	LastMove     map[string]time.Time
	MovesLast24h int
}

func (s *Store) RebalanceHistory(ctx context.Context, userID string, since time.Time, now time.Time) (History, error) {
	const q = `
		SELECT asset, created_at
		FROM executions
		WHERE user_id = $1 AND kind = 'rebalance' AND status <> 'failed' AND created_at >= $2
		ORDER BY created_at DESC`
	h := History{LastMove: map[string]time.Time{}}
	rows, err := s.pool.Query(ctx, q, userID, since)
	if err != nil {
		return h, fmt.Errorf("keeper store: history: %w", err)
	}
	defer rows.Close()

	dayAgo := now.Add(-24 * time.Hour)
	for rows.Next() {
		var asset string
		var at time.Time
		if err := rows.Scan(&asset, &at); err != nil {
			return h, err
		}
		if _, seen := h.LastMove[asset]; !seen {
			h.LastMove[asset] = at
		}
		if at.After(dayAgo) {
			h.MovesLast24h++
		}
	}
	return h, rows.Err()
}

type PendingExecution struct {
	ID        string
	UserID    string
	BasketID  string
	Kind      string
	Asset     string
	ToVenue   string
	FromVenue string
	AmountUSD float64
	TxHash    string
	CreatedAt time.Time
}

func (s *Store) PendingExecutions(ctx context.Context, olderThan time.Time, limit int) ([]PendingExecution, error) {
	const q = `
		SELECT id::text, user_id::text, COALESCE(basket_id::text, ''), kind, asset,
		       COALESCE(to_venue, ''), COALESCE(from_venue, ''), amount_usd, tx_hash, created_at
		FROM executions
		WHERE status = 'pending' AND tx_hash IS NOT NULL AND created_at <= $1
		ORDER BY created_at
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("keeper store: pending executions: %w", err)
	}
	defer rows.Close()

	var out []PendingExecution
	for rows.Next() {
		var p PendingExecution
		if err := rows.Scan(&p.ID, &p.UserID, &p.BasketID, &p.Kind, &p.Asset,
			&p.ToVenue, &p.FromVenue, &p.AmountUSD, &p.TxHash, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ResolveExecution(ctx context.Context, id, status string, errMsg *string) (bool, error) {
	const q = `
		UPDATE executions SET status = $2, error = COALESCE($3, error), updated_at = now()
		WHERE id = $1 AND status = 'pending'`
	tag, err := s.pool.Exec(ctx, q, id, status, errMsg)
	if err != nil {
		return false, fmt.Errorf("keeper store: resolve execution: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) UpsertPosition(ctx context.Context, p Position, userID, basketID, project string, entryAPY *float64) error {
	const q = `
		INSERT INTO positions (user_id, basket_id, asset, venue_id, chain, project, amount_usd, entry_apy, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, COALESCE($8, 0), now())
		ON CONFLICT (user_id, basket_id, asset) DO UPDATE SET
			venue_id   = EXCLUDED.venue_id,
			chain      = EXCLUDED.chain,
			project    = EXCLUDED.project,
			amount_usd = EXCLUDED.amount_usd,
			entry_apy  = COALESCE($8, positions.entry_apy),
			updated_at = now()`
	_, err := s.pool.Exec(ctx, q, userID, basketID, p.Asset, p.VenueID, p.Chain, project, p.AmountUSD, entryAPY)
	if err != nil {
		return fmt.Errorf("keeper store: upsert position: %w", err)
	}
	return nil
}
