package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/client"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/rpc"
	"github.com/snehendu098/sweem-basket/services/keeper/internal/store"
)

// Receipts is the chain read the sweeper needs. A nil receipt with a nil error
// means "not mined yet", which is not a failure.
type Receipts interface {
	TransactionReceipt(ctx context.Context, chainID int, txHash string) (*rpc.Receipt, error)
}

// SweepStats is the audit surface for the sweeper.
type SweepStats struct {
	Checked      int `json:"checked"`
	Confirmed    int `json:"confirmed"`
	Failed       int `json:"failed"`
	StillPending int `json:"still_pending"`
	GivenUp      int `json:"given_up"`
	Errors       int `json:"errors"`
}

// sweepPending resolves executions the wallet service left `pending`: the
// executor's receipt poll timed out, so the transaction's fate — and whether
// the user's money actually moved — is still unknown to our records. This runs
// before the drift evaluation: reasoning about positions we know are stale is
// how the keeper would act on money that is not where it thinks it is.
func (e *Engine) sweepPending(ctx context.Context, venues map[string][]client.Venue) SweepStats {
	var st SweepStats
	now := e.now()

	rows, err := e.Store.PendingExecutions(ctx, now.Add(-e.PendingMinAge), 200)
	if err != nil {
		st.Errors++
		e.Log.Error("sweep: load pending", "err", err)
		return st
	}

	for _, row := range rows {
		st.Checked++
		// The chain comes from the venue the execution targeted, not from a
		// global setting: a pending mainnet transaction must be looked up on
		// mainnet even while the process also serves Sepolia.
		chain, _ := splitVenueID(row.ToVenue)
		if chain == "" {
			chain, _ = splitVenueID(row.FromVenue)
		}
		chainID, known := chains.ID(chain)
		if !known {
			st.Errors++
			e.Log.Error("sweep: execution names a chain we do not serve",
				"execution_id", row.ID, "tx_hash", row.TxHash, "chain", chain)
			continue
		}
		receipt, err := e.Receipts.TransactionReceipt(ctx, chainID, row.TxHash)
		if err != nil {
			st.Errors++
			e.Log.Error("sweep: receipt", "execution_id", row.ID, "tx_hash", row.TxHash, "err", err)
			continue
		}

		age := now.Sub(row.CreatedAt)
		switch {
		case receipt == nil && age > e.PendingGiveUp:
			// A transaction nobody has seen for this long is worth a human.
			e.Log.Error("sweep: giving up on unobserved transaction",
				"execution_id", row.ID, "tx_hash", row.TxHash, "user_id", row.UserID,
				"asset", row.Asset, "amount_usd", row.AmountUSD, "age", age.Truncate(time.Minute))
			msg := fmt.Sprintf("transaction %s was never observed on chain after %s", row.TxHash, age.Truncate(time.Minute))
			if e.resolve(ctx, row, "failed", &msg, nil, &st) {
				st.GivenUp++
			}
		case receipt == nil:
			st.StillPending++
			e.Log.Info("sweep: still pending", "execution_id", row.ID, "tx_hash", row.TxHash,
				"age", age.Truncate(time.Second))
		case !receipt.Success():
			msg := fmt.Sprintf("transaction %s reverted on chain", row.TxHash)
			e.Log.Warn("sweep: reverted", "execution_id", row.ID, "tx_hash", row.TxHash)
			if e.resolve(ctx, row, "failed", &msg, nil, &st) {
				st.Failed++
			}
		default:
			if e.resolve(ctx, row, "confirmed", nil, venues, &st) {
				st.Confirmed++
			}
		}
	}

	if st.Checked > 0 {
		e.Log.Info("sweep complete", "checked", st.Checked, "confirmed", st.Confirmed,
			"failed", st.Failed, "still_pending", st.StillPending, "given_up", st.GivenUp,
			"dry_run", e.DryRun)
	}
	return st
}

// resolve claims the row and, on confirmation, writes the position the wallet
// service withheld. Claiming first is deliberate: the conditional update is the
// only lock, so no second instance — and no second pass — can write the
// position twice.
func (e *Engine) resolve(ctx context.Context, row store.PendingExecution, status string, errMsg *string, venues map[string][]client.Venue, st *SweepStats) bool {
	if e.DryRun {
		e.Log.Info("dry run: would resolve execution",
			"execution_id", row.ID, "tx_hash", row.TxHash, "status", status,
			"user_id", row.UserID, "asset", row.Asset, "to_venue", row.ToVenue,
			"amount_usd", row.AmountUSD)
		return true
	}

	claimed, err := e.Store.ResolveExecution(ctx, row.ID, status, errMsg)
	if err != nil {
		st.Errors++
		e.Log.Error("sweep: resolve", "execution_id", row.ID, "err", err)
		return false
	}
	if !claimed {
		// Another keeper instance took it. Not an error, and not ours to write.
		e.Log.Info("sweep: already resolved elsewhere", "execution_id", row.ID)
		return false
	}
	e.Log.Info("sweep: resolved", "execution_id", row.ID, "tx_hash", row.TxHash, "status", status)

	if status != "confirmed" {
		return true
	}
	// Only a move *into* a venue produces a position. Withdrawals have no
	// destination, so there is nothing to write.
	if row.ToVenue == "" || row.BasketID == "" {
		e.Log.Warn("sweep: confirmed with no destination venue; position untouched",
			"execution_id", row.ID, "kind", row.Kind)
		return true
	}

	chain, project := splitVenueID(row.ToVenue)
	pos := store.Position{
		Asset:     row.Asset,
		VenueID:   row.ToVenue,
		Chain:     chain,
		AmountUSD: row.AmountUSD,
	}
	if err := e.Store.UpsertPosition(ctx, pos, row.UserID, row.BasketID, project, e.venueAPY(ctx, venues, row, chain)); err != nil {
		// The money moved; only our bookkeeping failed. Say so loudly — the
		// next drift pass will reason about a position that is now wrong.
		st.Errors++
		e.Log.Error("sweep: write position", "execution_id", row.ID, "err", err)
	}
	return true
}

// venueAPY finds the live APY of the venue the money landed in. Returns nil
// when market-data does not currently carry it: keeping the row's existing
// entry_apy is honest, inventing one is not.
func (e *Engine) venueAPY(ctx context.Context, venues map[string][]client.Venue, row store.PendingExecution, chain string) *float64 {
	if venues == nil {
		return nil
	}
	key := row.Asset + "@" + chain
	list, ok := venues[key]
	if !ok {
		var err error
		if list, err = e.Market.Venues(ctx, row.Asset, chain, e.MinVenueTVL, 20); err != nil {
			e.Log.Warn("sweep: venue lookup", "asset", row.Asset, "err", err)
			return nil
		}
		venues[key] = list
	}
	for _, v := range list {
		if v.ID == row.ToVenue {
			apy := v.APY
			return &apy
		}
	}
	return nil
}

// splitVenueID unpacks market-data's "chain:project:pool" venue ID. The
// executions row carries the venue but not its parts.
func splitVenueID(id string) (chain, project string) {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
