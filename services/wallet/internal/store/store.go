package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) UpsertUser(ctx context.Context, did, wallet, privyWalletID string) (User, error) {
	const q = `
		INSERT INTO users (privy_did, wallet_address, privy_wallet_id)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (privy_did) DO UPDATE SET
			wallet_address  = EXCLUDED.wallet_address,
			privy_wallet_id = COALESCE(EXCLUDED.privy_wallet_id, users.privy_wallet_id)
		RETURNING id, privy_did, wallet_address, COALESCE(privy_wallet_id, ''), delegated, created_at`
	var u User
	err := s.pool.QueryRow(ctx, q, did, wallet, privyWalletID).
		Scan(&u.ID, &u.PrivyDID, &u.WalletAddress, &u.PrivyWalletID, &u.Delegated, &u.CreatedAt)
	return u, err
}

func (s *Store) UserByDID(ctx context.Context, did string) (User, error) {
	const q = `SELECT id, privy_did, wallet_address, COALESCE(privy_wallet_id, ''), delegated, created_at
	           FROM users WHERE privy_did = $1`
	var u User
	err := s.pool.QueryRow(ctx, q, did).
		Scan(&u.ID, &u.PrivyDID, &u.WalletAddress, &u.PrivyWalletID, &u.Delegated, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) SetDelegated(ctx context.Context, userID string, delegated bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET delegated = $2 WHERE id = $1`, userID, delegated)
	return err
}

func (s *Store) CreateBasket(ctx context.Context, b Basket) (Basket, error) {
	if err := ValidateWeights(b.Weights); err != nil {
		return Basket{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Basket{}, err
	}
	defer tx.Rollback(ctx)

	const insBasket = `
		INSERT INTO baskets (creator_id, name, description, chain, is_public, fee_bps)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, created_at`
	if err := tx.QueryRow(ctx, insBasket,
		b.CreatorID, b.Name, b.Description, b.Chain, b.IsPublic, b.FeeBps,
	).Scan(&b.ID, &b.CreatedAt); err != nil {
		return Basket{}, fmt.Errorf("store: insert basket: %w", err)
	}

	for _, w := range b.Weights {
		if _, err := tx.Exec(ctx,
			`INSERT INTO basket_weights (basket_id, asset, weight_bps, venue_id) VALUES ($1,$2,$3,$4)`,
			b.ID, w.Asset, w.WeightBps, nullable(w.VenueID),
		); err != nil {
			return Basket{}, fmt.Errorf("store: insert weight %s: %w", w.Asset, err)
		}
	}
	return b, tx.Commit(ctx)
}

var ErrBasketInUse = errors.New("store: basket has positions")

// Only an empty basket goes: a basket someone has money in is a record, not a
// draft, and subscribers would lose theirs with it.
func (s *Store) DeleteBasket(ctx context.Context, id, ownerID string) error {
	var held int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE basket_id = $1`, id,
	).Scan(&held); err != nil {
		return err
	}
	if held > 0 {
		return ErrBasketInUse
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM baskets WHERE id = $1 AND creator_id = $2`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Basket(ctx context.Context, id string) (Basket, error) {
	const q = `SELECT id, creator_id, name, description, chain, is_public, fee_bps, created_at
	           FROM baskets WHERE id = $1`
	var b Basket
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&b.ID, &b.CreatorID, &b.Name, &b.Description, &b.Chain, &b.IsPublic, &b.FeeBps, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Basket{}, ErrNotFound
	}
	if err != nil {
		return Basket{}, err
	}
	b.Weights, err = s.weights(ctx, id)
	return b, err
}

func (s *Store) weights(ctx context.Context, basketID string) ([]Weight, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT asset, weight_bps, venue_id FROM basket_weights WHERE basket_id = $1 ORDER BY weight_bps DESC`,
		basketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Weight{}
	for rows.Next() {
		var w Weight
		var venueID *string
		if err := rows.Scan(&w.Asset, &w.WeightBps, &venueID); err != nil {
			return nil, err
		}
		w.VenueID = deref(venueID)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) ListBaskets(ctx context.Context, ownerID string, publicOnly bool, limit int) ([]Basket, error) {
	q := `SELECT id, creator_id, name, description, chain, is_public, fee_bps, created_at FROM baskets`
	args := []any{}
	switch {
	case ownerID != "":
		q += ` WHERE creator_id = $1`
		args = append(args, ownerID)
	case publicOnly:
		q += ` WHERE is_public`
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Basket{}
	for rows.Next() {
		var b Basket
		if err := rows.Scan(&b.ID, &b.CreatorID, &b.Name, &b.Description,
			&b.Chain, &b.IsPublic, &b.FeeBps, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	byBasket, err := s.weightsFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if w, ok := byBasket[out[i].ID]; ok {
			out[i].Weights = w
		} else {
			out[i].Weights = []Weight{}
		}
	}
	return out, nil
}

func (s *Store) weightsFor(ctx context.Context, basketIDs []string) (map[string][]Weight, error) {
	if len(basketIDs) == 0 {
		return map[string][]Weight{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT basket_id, asset, weight_bps, venue_id FROM basket_weights
		 WHERE basket_id = ANY($1) ORDER BY basket_id, weight_bps DESC`,
		basketIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	flat := []basketWeight{}
	for rows.Next() {
		var bw basketWeight
		var venueID *string
		if err := rows.Scan(&bw.BasketID, &bw.Asset, &bw.WeightBps, &venueID); err != nil {
			return nil, err
		}
		bw.VenueID = deref(venueID)
		flat = append(flat, bw)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return groupWeights(flat), nil
}

type basketWeight struct {
	BasketID  string
	Asset     string
	WeightBps int
	VenueID   string
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func groupWeights(rows []basketWeight) map[string][]Weight {
	out := make(map[string][]Weight)
	for _, r := range rows {
		out[r.BasketID] = append(out[r.BasketID], Weight{Asset: r.Asset, WeightBps: r.WeightBps, VenueID: r.VenueID})
	}
	return out
}

func (s *Store) SubscribedTo(ctx context.Context, userID string, basketIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if userID == "" || len(basketIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT basket_id FROM subscriptions
		 WHERE user_id = $1 AND basket_id = ANY($2) AND status = 'active'`,
		userID, basketIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) Subscribe(ctx context.Context, userID, basketID string) (Subscription, error) {
	const q = `
		INSERT INTO subscriptions (user_id, basket_id) VALUES ($1,$2)
		ON CONFLICT (user_id, basket_id) DO UPDATE SET status = 'active'
		RETURNING id, user_id, basket_id, status, created_at`
	var sub Subscription
	err := s.pool.QueryRow(ctx, q, userID, basketID).
		Scan(&sub.ID, &sub.UserID, &sub.BasketID, &sub.Status, &sub.CreatedAt)
	return sub, err
}

func (s *Store) Unsubscribe(ctx context.Context, userID, basketID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE subscriptions SET status = 'exited' WHERE user_id = $1 AND basket_id = $2`,
		userID, basketID)
	return err
}

func (s *Store) UpsertPosition(ctx context.Context, p Position) error {
	const q = `
		INSERT INTO positions (user_id, basket_id, asset, venue_id, chain, project, amount_usd, entry_apy, entry_price_usd, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
		ON CONFLICT (user_id, basket_id, asset) DO UPDATE SET
			venue_id = EXCLUDED.venue_id,
			chain = EXCLUDED.chain,
			project = EXCLUDED.project,
			amount_usd = EXCLUDED.amount_usd,
			entry_apy = EXCLUDED.entry_apy,
			entry_price_usd = CASE
				WHEN EXCLUDED.entry_price_usd > 0 THEN EXCLUDED.entry_price_usd
				ELSE positions.entry_price_usd
			END,
			updated_at = now()`
	_, err := s.pool.Exec(ctx, q,
		p.UserID, p.BasketID, p.Asset, p.VenueID, p.Chain, p.Project, p.AmountUSD, p.EntryAPY,
		p.EntryPriceUSD)
	return err
}

func (s *Store) DeletePosition(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM positions WHERE id = $1`, id)
	return err
}

func (s *Store) Positions(ctx context.Context, userID, basketID string) ([]Position, error) {
	q := `SELECT id, user_id, basket_id, asset, venue_id, chain, project, amount_usd, entry_apy,
	             entry_price_usd, updated_at
	      FROM positions WHERE user_id = $1`
	args := []any{userID}
	if basketID != "" {
		q += ` AND basket_id = $2`
		args = append(args, basketID)
	}
	q += ` ORDER BY amount_usd DESC`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Position{}
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.ID, &p.UserID, &p.BasketID, &p.Asset, &p.VenueID,
			&p.Chain, &p.Project, &p.AmountUSD, &p.EntryAPY, &p.EntryPriceUSD,
			&p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) CreateExecution(ctx context.Context, e Execution) (Execution, error) {
	const q = `
		INSERT INTO executions (user_id, basket_id, kind, asset, from_venue, to_venue, amount_usd, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'pending')
		RETURNING id, status, created_at`
	err := s.pool.QueryRow(ctx, q,
		e.UserID, e.BasketID, e.Kind, e.Asset, e.FromVenue, e.ToVenue, e.AmountUSD,
	).Scan(&e.ID, &e.Status, &e.CreatedAt)
	return e, err
}

func (s *Store) UpdateExecution(ctx context.Context, id, status string, txHash, errMsg *string, steps json.RawMessage) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE executions SET status=$2, tx_hash=COALESCE($3,tx_hash), error=$4,
		        steps=COALESCE($5, steps), updated_at=now() WHERE id=$1`,
		id, status, txHash, errMsg, steps)
	return err
}

func (s *Store) Executions(ctx context.Context, userID string, limit int) ([]Execution, error) {
	const q = `SELECT id, user_id, basket_id, kind, asset, from_venue, to_venue,
	                  amount_usd, tx_hash, status, error, steps, created_at
	           FROM executions WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`
	rows, err := s.pool.Query(ctx, q, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Execution{}
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.UserID, &e.BasketID, &e.Kind, &e.Asset,
			&e.FromVenue, &e.ToVenue, &e.AmountUSD, &e.TxHash, &e.Status, &e.Error,
			&e.Steps, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
