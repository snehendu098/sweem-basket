package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/executor"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/marketdata"
	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

// allocate splits totalCents across weights by the largest-remainder method.
//
// Naive proportional arithmetic loses or invents fractions of a cent, and a
// split that does not sum back to what the user asked for is money we cannot
// account for. Integer cents plus leftovers to the biggest remainders makes the
// sum exact by construction. Shared by deposits (weights in bps) and withdraws
// (weights in position value).
func allocate(totalCents int64, weights []int64) []int64 {
	cents := make([]int64, len(weights))
	var totalWeight int64
	for _, w := range weights {
		if w > 0 {
			totalWeight += w
		}
	}
	if totalCents <= 0 || totalWeight <= 0 {
		return cents
	}

	type rem struct {
		i    int
		frac int64 // leftover numerator, denominator totalWeight
	}
	rems := make([]rem, 0, len(weights))
	var assigned int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		n := totalCents * w
		cents[i] = n / totalWeight
		assigned += cents[i]
		rems = append(rems, rem{i: i, frac: n % totalWeight})
	}
	// Stable order: bigger remainder first, then original position, so the same
	// input always splits the same way.
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := int64(0); k < totalCents-assigned && len(rems) > 0; k++ {
		cents[rems[k%int64(len(rems))].i]++
	}
	return cents
}

func centsToUSD(cents []int64) []float64 {
	out := make([]float64, len(cents))
	for i, c := range cents {
		out[i] = float64(c) / 100
	}
	return out
}

// splitAmounts divides a deposit across basket weights, in whole cents.
func splitAmounts(amountUSD float64, ws []store.Weight) []float64 {
	weights := make([]int64, len(ws))
	for i, w := range ws {
		weights[i] = int64(w.WeightBps)
	}
	return centsToUSD(allocate(int64(math.Round(amountUSD*100)), weights))
}

// splitProportional divides a withdrawal across positions in proportion to what
// each currently holds, so a partial exit leaves the basket's shape intact.
func splitProportional(amountUSD float64, values []float64) []float64 {
	weights := make([]int64, len(values))
	for i, v := range values {
		weights[i] = int64(math.Round(v * 100))
	}
	return centsToUSD(allocate(int64(math.Round(amountUSD*100)), weights))
}

// planLegs resolves a basket's weights against live venue data. Shared by the
// read-only /plan preview and the /deposit write path, so the money moves
// exactly where the preview said it would.
func (s *Server) planLegs(r *http.Request, b store.Basket, amountUSD float64) ([]PlanLeg, float64) {
	amounts := splitAmounts(amountUSD, b.Weights)
	legs := make([]PlanLeg, 0, len(b.Weights))
	var blended float64
	for i, weight := range b.Weights {
		leg := PlanLeg{
			Asset:     weight.Asset,
			WeightBps: weight.WeightBps,
			AmountUSD: amounts[i],
		}
		// Price first. Routing money on an asset we cannot value means moving
		// it on a number we invented, so an unpriceable leg never gets a venue.
		price, perr := s.priceUSD(r.Context(), b.Chain, weight.Asset)
		if perr != nil {
			leg.Reason = priceReason(weight.Asset, perr)
			legs = append(legs, leg)
			continue
		}
		leg.PriceUSD = &price.USD
		if price.USD > 0 {
			tokens := leg.AmountUSD / price.USD
			leg.AmountToken = &tokens
		}

		// Fundability before rate. A leg whose asset cannot be bought with the
		// deposited USDC is not a worse route, it is no route: proposing it
		// would surface as an unlisted-path error at submission, after the user
		// has committed.
		if serr := s.swapFundable(r.Context(), weight.Asset, b.Chain); serr != nil {
			if errors.Is(serr, ErrNoSwapPath) {
				leg.Reason = fmt.Sprintf("no %s swap route to %s on %s; this leg cannot be funded",
					QuoteAsset, weight.Asset, b.Chain)
			} else {
				s.Log.Warn("swap path lookup", "asset", weight.Asset, "err", serr)
				leg.Reason = "executor swap routes unavailable; this leg cannot be funded"
			}
			legs = append(legs, leg)
			continue
		}

		v, note, err := s.bestRoutable(r.Context(), weight.Asset, b.Chain)
		switch {
		case errors.Is(err, marketdata.ErrNoVenue):
			leg.Reason = "no venue meets the liquidity floor; funds stay idle"
		case errors.Is(err, ErrNoRoutableVenue):
			// Indexed venues exist, none of them executable. Say which, rather
			// than reporting "no venue" for something the user can see a rate for.
			leg.Reason = "no venue for this asset can be transacted by the executor; funds stay idle"
		case err != nil:
			s.Log.Warn("routable venue lookup", "asset", weight.Asset, "err", err)
			leg.Reason = "market data unavailable"
		default:
			leg.Venue = &v
			leg.Reason = "highest net APY above the TVL floor"
			if note != "" {
				leg.Reason = note
			}
			blended += v.APY * float64(weight.WeightBps) / float64(store.TotalBps)
		}
		legs = append(legs, leg)
	}
	return legs, blended
}

// LegResult is the outcome of one leg of a deposit or rebalance. Every leg is
// reported independently — a failed leg never hides a succeeded one.
type LegResult struct {
	Asset       string  `json:"asset"`
	AmountUSD   float64 `json:"amount_usd"`
	FromVenueID string  `json:"from_venue_id,omitempty"`
	VenueID     string  `json:"venue_id,omitempty"`
	Project     string  `json:"project,omitempty"`
	APY         float64 `json:"apy,omitempty"`
	ExecutionID string  `json:"execution_id,omitempty"`
	TxHash      string  `json:"tx_hash,omitempty"`
	// Status is submitted | pending | failed | skipped.
	Status string          `json:"status"`
	Reason string          `json:"reason,omitempty"`
	Steps  []executor.Step `json:"steps,omitempty"`
}

// canExecute rejects callers the executor cannot act for. Both the delegation
// and Privy's wallet ID are required; say which one is missing.
func (s *Server) canExecute(w http.ResponseWriter, u store.User) bool {
	var missing string
	switch {
	case !u.Delegated:
		// Privy only issues the server wallet ID once delegation exists, so
		// asking for the ID first would be asking for something that does not
		// exist yet. Delegation is always the first step.
		missing = "wallet delegation is missing; grant it in Privy, then POST /v1/me again with the wallet ID Privy returns"
	case u.PrivyWalletID == "":
		missing = "wallet delegation is granted but we have not been told the Privy wallet ID yet; POST /v1/me again to send it"
	default:
		return true
	}
	writeErr(w, http.StatusPreconditionFailed, "cannot execute: "+missing)
	return false
}

// Leg statuses reported to the caller. Deliberately three-valued: a receipt
// poll that timed out is not a failure, and collapsing it into one would invite
// a second submission of money that already moved.
const (
	legSubmitted = "submitted"
	legPending   = "pending"
	legFailed    = "failed"
	legSkipped   = "skipped"
)

// IdleVenueID marks a position whose funds are sitting unplaced in the user's
// own wallet — a multi-step sequence that withdrew and then failed to
// redeposit. The money is not lost, but it is not earning either, and the
// position row must not keep claiming the old venue.
// ponytail: sentinel venue ID rather than a new column; add a real `state`
// column if positions grow more lifecycle states than "placed" and "idle".
const IdleVenueID = "idle:wallet"

// legOutcome maps an executor reply to the row status we persist and the leg
// status we report. Pure, so the three-way branch is testable.
func legOutcome(resp executor.RouteResponse, err error) (dbStatus, legStatus, reason string) {
	switch {
	case err != nil:
		return "failed", legFailed, err.Error()
	case resp.Status == "pending":
		// Receipt poll timed out. Keep the row pending: the transaction is
		// probably still in the mempool, and calling it failed would be a lie
		// that costs money to correct.
		return "pending", legPending, "submitted but not yet confirmed; receipt poll timed out"
	case resp.Status == "failed":
		if resp.Error == "" {
			return "failed", legFailed, "executor reported failure"
		}
		return "failed", legFailed, resp.Error
	case resp.Status == "confirmed":
		return "confirmed", legSubmitted, ""
	default:
		return "submitted", legSubmitted, ""
	}
}

// fundsUnplaced reports whether a sequence moved money out of its source before
// it stopped. A rebalance is withdraw → approve → deposit: a confirmed step
// followed by a failure leaves the funds idle in the wallet.
func fundsUnplaced(resp executor.RouteResponse) bool {
	for _, st := range resp.Steps {
		if st.Outcome == "confirmed" {
			return true
		}
	}
	return false
}

// run performs one executor call with its execution row. The row is written
// BEFORE the call so a crash mid-flight leaves a visible `pending` row rather
// than an invisible transaction.
func (s *Server) run(r *http.Request, u store.User, e store.Execution, req executor.RouteRequest) (store.Execution, executor.RouteResponse, LegResult) {
	ctx := r.Context()
	e, err := s.Store.CreateExecution(ctx, e)
	if err != nil {
		// Never call the executor without a row to record it against — an
		// untracked transaction is money nobody can account for.
		s.Log.Error("create execution", "asset", e.Asset, "err", err)
		return e, executor.RouteResponse{}, LegResult{Status: legFailed, Reason: "could not record execution; nothing was submitted"}
	}
	// The executor serves both chains and checks that every venue it is handed
	// lives on the chain named here. Resolving the label once, here, is what
	// makes that check meaningful: nothing downstream guesses a chain.
	chainID, cerr := chainOf(req.Chain)
	if cerr != nil {
		s.Log.Error("unroutable chain", "chain", req.Chain, "asset", e.Asset, "err", cerr)
		return e, executor.RouteResponse{}, LegResult{
			Status: legFailed,
			Reason: "basket is on an unsupported chain (" + req.Chain + "); nothing was submitted",
		}
	}
	req.ChainID = chainID
	req.ExecutionID = e.ID
	req.UserWallet = u.WalletAddress
	req.PrivyDID = u.PrivyDID
	req.PrivyWalletID = u.PrivyWalletID
	req.MaxSlippageBp = s.MaxSlippageBps

	resp, err := s.Executor.Route(ctx, req)
	dbStatus, legStatus, reason := legOutcome(resp, err)

	var steps json.RawMessage
	if len(resp.Steps) > 0 {
		if raw, merr := json.Marshal(resp.Steps); merr == nil {
			steps = raw
		}
	}
	var txHash, errMsg *string
	if resp.TxHash != "" {
		txHash = &resp.TxHash
	}
	if legStatus == legFailed && reason != "" {
		errMsg = &reason
	}
	if uerr := s.Store.UpdateExecution(ctx, e.ID, dbStatus, txHash, errMsg, steps); uerr != nil {
		// The chain call already happened; log loudly and still report it.
		s.Log.Error("update execution", "execution_id", e.ID, "status", dbStatus, "err", uerr)
	}
	return e, resp, LegResult{
		ExecutionID: e.ID,
		TxHash:      resp.TxHash,
		Status:      legStatus,
		Reason:      reason,
		Steps:       resp.Steps,
	}
}

// deposit routes a USDC deposit across a basket's weights.
//
// Partial failure is the normal case: legs are independent, and one failing
// leg must not roll back or hide the others. Every leg gets its own execution
// row and its own entry in the response.
func (s *Server) deposit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	var body struct {
		AmountUSD float64 `json:"amount_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AmountUSD <= 0 {
		writeErr(w, http.StatusBadRequest, "amount_usd must be greater than 0")
		return
	}
	b, err := s.Store.Basket(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "basket not found")
		return
	}
	if err != nil {
		s.Log.Error("get basket", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	legs, _ := s.planLegs(r, b, body.AmountUSD)
	results := make([]LegResult, 0, len(legs))
	var submittedUSD float64
	failed, pending := 0, 0

	for _, leg := range legs {
		res := LegResult{Asset: leg.Asset, AmountUSD: leg.AmountUSD}
		if leg.Venue == nil || leg.AmountUSD <= 0 {
			res.Status, res.Reason = legSkipped, leg.Reason
			if leg.AmountUSD <= 0 {
				res.Reason = "leg rounds to zero"
			}
			results = append(results, res)
			continue
		}
		res.VenueID, res.Project, res.APY = leg.Venue.ID, leg.Venue.Project, leg.Venue.APY

		basketID := b.ID
		_, _, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &basketID,
			Kind:      "deposit",
			Asset:     leg.Asset,
			ToVenue:   &leg.Venue.ID,
			AmountUSD: leg.AmountUSD,
		}, executor.RouteRequest{
			Chain:     b.Chain,
			Asset:     leg.Asset,
			AmountUSD: leg.AmountUSD,
			Action:    "deposit",
			ToVenueID: leg.Venue.ID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("deposit leg failed", "asset", leg.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			results = append(results, res)
			continue
		case legPending:
			// The money may or may not have arrived. Recording a position now
			// would claim yield the user might not be earning.
			pending++
			s.Log.Warn("deposit leg pending", "asset", leg.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		submittedUSD += leg.AmountUSD

		if err := s.Store.UpsertPosition(r.Context(), store.Position{
			UserID:    u.ID,
			BasketID:  b.ID,
			Asset:     leg.Asset,
			VenueID:   leg.Venue.ID,
			Chain:     leg.Venue.Chain,
			Project:   leg.Venue.Project,
			AmountUSD: leg.AmountUSD,
			EntryAPY:  leg.Venue.APY,
		}); err != nil {
			// The money moved; only our bookkeeping failed. Say so.
			s.Log.Error("upsert position", "asset", leg.Asset, "err", err)
			res.Reason = "submitted, but the position record failed to save"
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     b.ID,
		"amount_usd":    body.AmountUSD,
		"submitted_usd": submittedUSD,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

// partitionWithdrawable separates positions that sit in a venue from those
// already idle in the user's wallet.
//
// Idle funds have no venue to withdraw from, so routing one would be a call
// against a venue that is not holding the money. Excluding them from the total
// also keeps them out of the proportional split, so a requested amount is drawn
// entirely from positions that can actually supply it.
func partitionWithdrawable(positions []store.Position) (withdrawable []store.Position, skipped []LegResult, available float64) {
	withdrawable = make([]store.Position, 0, len(positions))
	skipped = make([]LegResult, 0)
	for _, p := range positions {
		if p.VenueID == IdleVenueID {
			skipped = append(skipped, LegResult{
				Asset: p.Asset, FromVenueID: p.VenueID, Status: legSkipped,
				Reason: "already idle in your wallet; nothing to withdraw from a venue",
			})
			continue
		}
		withdrawable = append(withdrawable, p)
		available += p.AmountUSD
	}
	return withdrawable, skipped, available
}

// withdraw takes money back out of venues and into the user's own wallet.
//
// The counterpart to deposit, and the reason a user is never trapped: exiting a
// subscription only stops future moves, it does not return funds. Same per-leg
// independence, same three-way outcome, same executions trail.
func (s *Server) withdraw(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	var body struct {
		AmountUSD float64 `json:"amount_usd"`
		All       bool    `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if !body.All && body.AmountUSD <= 0 {
		writeErr(w, http.StatusBadRequest, "send amount_usd greater than 0, or all: true")
		return
	}

	basketID := r.PathValue("id")
	positions, err := s.Store.Positions(r.Context(), u.ID, basketID)
	if err != nil {
		s.Log.Error("positions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	withdrawable, skipped, available := partitionWithdrawable(positions)

	amount := body.AmountUSD
	if body.All {
		amount = available
	}
	if amount > available+0.005 {
		// Refuse rather than quietly withdraw less than asked: the caller must
		// know the difference between "took 500" and "took what was there".
		writeErr(w, http.StatusBadRequest, fmt.Sprintf(
			"amount_usd %.2f exceeds the %.2f held in this basket", amount, available))
		return
	}
	if len(withdrawable) == 0 || amount <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"basket_id": basketID, "amount_usd": 0, "withdrawn_usd": float64(0),
			"failed_legs": 0, "pending_legs": 0, "legs": skipped,
		})
		return
	}

	values := make([]float64, len(withdrawable))
	for i, p := range withdrawable {
		values[i] = p.AmountUSD
	}
	amounts := splitProportional(amount, values)

	results := make([]LegResult, 0, len(positions))
	results = append(results, skipped...)
	var withdrawnUSD float64
	failed, pending := 0, 0

	for i, p := range withdrawable {
		legUSD := amounts[i]
		res := LegResult{Asset: p.Asset, AmountUSD: legUSD, FromVenueID: p.VenueID, Project: p.Project}
		if legUSD <= 0 {
			res.Status, res.Reason = legSkipped, "leg rounds to zero"
			results = append(results, res)
			continue
		}

		bid := p.BasketID
		_, _, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &bid,
			Kind:      "withdraw",
			Asset:     p.Asset,
			FromVenue: &p.VenueID,
			AmountUSD: legUSD,
		}, executor.RouteRequest{
			Chain:       p.Chain,
			Asset:       p.Asset,
			AmountUSD:   legUSD,
			Action:      "withdraw",
			FromVenueID: p.VenueID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("withdraw leg failed", "asset", p.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			results = append(results, res)
			continue
		case legPending:
			// The funds may or may not have left the venue. Reducing the
			// position now would understate what the user still holds there.
			pending++
			s.Log.Warn("withdraw leg pending", "asset", p.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		withdrawnUSD += legUSD

		// Round to the cent so a fully drained position does not survive as a
		// fraction-of-a-cent ghost row.
		remaining := math.Round((p.AmountUSD-legUSD)*100) / 100
		if remaining <= 0 {
			if err := s.Store.DeletePosition(r.Context(), p.ID); err != nil {
				s.Log.Error("delete position", "asset", p.Asset, "err", err)
				res.Reason = "withdrawn, but the position record failed to clear"
			}
		} else {
			p.AmountUSD = remaining
			if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
				s.Log.Error("upsert position", "asset", p.Asset, "err", err)
				res.Reason = "withdrawn, but the position record failed to save"
			}
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     basketID,
		"amount_usd":    amount,
		"withdrawn_usd": withdrawnUSD,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

// rebalance moves positions whose drift exceeds the threshold into the best
// venue available now. Same per-leg independence as deposit.
func (s *Server) rebalance(w http.ResponseWriter, r *http.Request) {
	u, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canExecute(w, u) {
		return
	}
	basketID := r.PathValue("id")
	// Distinguish automated moves from ones the user asked for.
	actor := "user"
	if c, ok := auth.FromContext(r.Context()); ok && c.ViaKeeper {
		actor = "keeper"
	}
	s.Log.Info("rebalance requested", "actor", actor, "user_id", u.ID, "basket_id", basketID)

	positions, err := s.Store.Positions(r.Context(), u.ID, basketID)
	if err != nil {
		s.Log.Error("positions", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	results := make([]LegResult, 0, len(positions))
	failed, pending, moved := 0, 0, 0
	for _, p := range positions {
		res := LegResult{Asset: p.Asset, AmountUSD: p.AmountUSD, FromVenueID: p.VenueID}
		best, note, err := s.bestRoutable(r.Context(), p.Asset, p.Chain)
		if err != nil {
			res.Status, res.Reason = legSkipped, "no better executable venue available"
			if errors.Is(err, ErrNoRoutableVenue) {
				res.Reason = "no venue for this asset can be transacted by the executor"
			}
			s.Log.Info("skip rebalance leg", "asset", p.Asset, "chain", p.Chain, "err", err)
			results = append(results, res)
			continue
		}
		_, drift := driftAPY(p, best)
		if !shouldRebalance(drift, s.RebalanceThresholdAPY) {
			res.Status = legSkipped
			res.Reason = "drift below threshold"
			res.VenueID, res.APY = best.ID, best.APY
			results = append(results, res)
			continue
		}
		res.VenueID, res.Project, res.APY = best.ID, best.Project, best.APY
		if note != "" {
			res.Reason = note
		}

		// Funds already sitting idle in the wallet have nothing to withdraw —
		// that is a plain deposit, not a venue-to-venue move.
		action, fromVenue := "rebalance", p.VenueID
		if p.VenueID == IdleVenueID {
			action, fromVenue = "deposit", ""
		}

		bid := p.BasketID
		_, resp, out := s.run(r, u, store.Execution{
			UserID:    u.ID,
			BasketID:  &bid,
			Kind:      "rebalance",
			Asset:     p.Asset,
			FromVenue: &p.VenueID,
			ToVenue:   &best.ID,
			AmountUSD: p.AmountUSD,
		}, executor.RouteRequest{
			Chain:       p.Chain,
			Asset:       p.Asset,
			AmountUSD:   p.AmountUSD,
			Action:      action,
			FromVenueID: fromVenue,
			ToVenueID:   best.ID,
		})
		res.ExecutionID, res.TxHash, res.Status, res.Reason, res.Steps =
			out.ExecutionID, out.TxHash, out.Status, out.Reason, out.Steps

		switch res.Status {
		case legFailed:
			failed++
			s.Log.Warn("rebalance leg failed", "actor", actor, "asset", p.Asset, "execution_id", res.ExecutionID, "reason", res.Reason)
			if fundsUnplaced(resp) {
				// The withdraw landed but the redeposit did not. The funds are
				// idle in the user's wallet — the position must say so rather
				// than keep claiming a venue that no longer holds the money.
				res.VenueID, res.Reason = IdleVenueID,
					"withdrew but failed to redeposit; funds are idle in your wallet"
				p.VenueID, p.Project, p.EntryAPY = IdleVenueID, "wallet", 0
				if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
					s.Log.Error("mark position idle", "asset", p.Asset, "err", err)
				}
			}
			results = append(results, res)
			continue
		case legPending:
			// Unknown where the money sits. Leave the position on its old venue
			// rather than guess; the executions row keeps the tx hash.
			pending++
			s.Log.Warn("rebalance leg pending", "asset", p.Asset, "execution_id", res.ExecutionID)
			results = append(results, res)
			continue
		}
		moved++

		p.VenueID, p.Chain, p.Project, p.EntryAPY = best.ID, best.Chain, best.Project, best.APY
		if err := s.Store.UpsertPosition(r.Context(), p); err != nil {
			s.Log.Error("upsert position", "asset", p.Asset, "err", err)
			res.Reason = "submitted, but the position record failed to save"
		}
		results = append(results, res)
	}

	writeJSON(w, settledStatus(failed, pending), map[string]any{
		"basket_id":     basketID,
		"threshold_apy": s.RebalanceThresholdAPY,
		"moved_legs":    moved,
		"failed_legs":   failed,
		"pending_legs":  pending,
		"legs":          results,
	})
}

// settledStatus is 200 only when every leg reached a settled, successful state.
// A pending leg is as unsettled as a failed one: the caller must not assume the
// money arrived.
func settledStatus(failed, pending int) int {
	if failed > 0 || pending > 0 {
		return http.StatusMultiStatus
	}
	return http.StatusOK
}

// shouldRebalance gates on drift only. Below the threshold the gas and slippage
// of moving cost more than the extra yield is worth.
func shouldRebalance(drift, threshold float64) bool { return drift > threshold }
