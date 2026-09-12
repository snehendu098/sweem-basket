package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

// basketReader is the slice of the store the unauthenticated routes touch.
// It exists so those routes can be exercised without a live database; Routes
// falls back to Server.Store when PublicStore is unset.
type basketReader interface {
	Basket(ctx context.Context, id string) (store.Basket, error)
	ListBaskets(ctx context.Context, ownerID string, publicOnly bool, limit int) ([]store.Basket, error)
}

const (
	publicListDefault = 50
	// publicListMax caps what one unauthenticated request can pull out of the
	// table. Discovery renders a page, not the catalogue.
	publicListMax = 100
)

// publicBasket is the deliberately narrow view served without a token. It is
// not store.Basket: that carries creator_id (an internal UUID) and subscribed
// (per-caller, meaningless with no caller). Creator identity is omitted
// entirely until there is a stable public handle to show.
type publicBasket struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Chain       string         `json:"chain"`
	FeeBps      int            `json:"fee_bps"`
	Weights     []store.Weight `json:"weights"`
	CreatedAt   time.Time      `json:"created_at"`
}

func publicView(b store.Basket) publicBasket {
	w := b.Weights
	if w == nil {
		w = []store.Weight{} // never marshal as null
	}
	return publicBasket{
		ID:          b.ID,
		Name:        b.Name,
		Description: b.Description,
		Chain:       b.Chain,
		FeeBps:      b.FeeBps,
		Weights:     w,
		CreatedAt:   b.CreatedAt,
	}
}

func (s *Server) publicBaskets() basketReader {
	if s.PublicStore != nil {
		return s.PublicStore
	}
	return s.Store
}

// clampLimit reads ?limit and pins it into [1, max]. Out of range is clamped
// rather than rejected: a discovery page asking for too much should still get
// a page.
func clampLimit(raw string, def, max int) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// publicListBaskets serves public baskets to callers with no token. Weights
// come batched from ListBaskets — an N+1 here would be free amplification.
func (s *Server) publicListBaskets(w http.ResponseWriter, r *http.Request) {
	limit := clampLimit(r.URL.Query().Get("limit"), publicListDefault, publicListMax)
	bs, err := s.publicBaskets().ListBaskets(r.Context(), "", true, limit)
	if err != nil {
		s.Log.Error("public list baskets", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]publicBasket, 0, len(bs))
	for _, b := range bs {
		out = append(out, publicView(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// publicGetBasket serves one public basket. A private basket answers 404, not
// 403: an anonymous caller must not be able to confirm that an id exists.
func (s *Server) publicGetBasket(w http.ResponseWriter, r *http.Request) {
	b, err := s.publicBaskets().Basket(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && !b.IsPublic) {
		writeErr(w, http.StatusNotFound, "basket not found")
		return
	}
	if err != nil {
		s.Log.Error("public get basket", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, publicView(b))
}

// publicSwapPaths serves the executor's swap allowlist verbatim: which pairs
// can be bought with the quote asset, on which chain. The client needs this to
// stop offering assets that cannot be funded — it was previously carrying a
// hardcoded per-chain guess, which was exact on Sepolia and wrong on mainnet.
//
// Unauthenticated because it is static configuration with no user in it, and
// the asset picker renders before a deposit is ever authorised. A failed fetch
// is 503, never an empty list: "nothing is swappable" and "we could not ask"
// must not look the same to the caller.
func (s *Server) publicSwapPaths(w http.ResponseWriter, r *http.Request) {
	paths, err := s.Executor.SwapPaths(r.Context())
	if err != nil {
		s.Log.Warn("swap paths", "err", err)
		writeErr(w, http.StatusServiceUnavailable, "swap routes unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"quote_asset": QuoteAsset,
		"count":       len(paths),
		"paths":       paths,
	})
}
