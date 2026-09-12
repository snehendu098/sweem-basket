package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

type basketReader interface {
	Basket(ctx context.Context, id string) (store.Basket, error)
	ListBaskets(ctx context.Context, ownerID string, publicOnly bool, limit int) ([]store.Basket, error)
}

const (
	publicListDefault = 50
	publicListMax     = 100
)

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
		w = []store.Weight{}
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
