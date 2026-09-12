package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/store"
)

// fakeBaskets records what the handler asked for and replays canned rows.
type fakeBaskets struct {
	byID      map[string]store.Basket
	list      []store.Basket
	gotLimit  int
	gotPublic bool
}

func (f *fakeBaskets) Basket(_ context.Context, id string) (store.Basket, error) {
	b, ok := f.byID[id]
	if !ok {
		return store.Basket{}, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeBaskets) ListBaskets(_ context.Context, _ string, publicOnly bool, limit int) ([]store.Basket, error) {
	f.gotLimit, f.gotPublic = limit, publicOnly
	return f.list, nil
}

func publicServer(f *fakeBaskets) http.Handler {
	s := &Server{PublicStore: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s.Routes()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	// No Authorization header, on purpose.
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPublicGetBasket(t *testing.T) {
	f := &fakeBaskets{byID: map[string]store.Basket{
		"pub": {ID: "pub", CreatorID: "user-uuid", Name: "Blue", IsPublic: true, FeeBps: 25,
			Weights: []store.Weight{{Asset: "USDC", WeightBps: 10000}}},
		"priv": {ID: "priv", CreatorID: "user-uuid", Name: "Secret"},
	}}
	h := publicServer(f)

	tests := []struct {
		name, path string
		want       int
	}{
		{"public basket is served", "/public/baskets/pub", http.StatusOK},
		{"private basket is hidden as missing", "/public/baskets/priv", http.StatusNotFound},
		{"unknown id", "/public/baskets/nope", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, h, tt.path)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

// The anonymous view must carry the basket, not the caller or the creator.
func TestPublicBasketOmitsCallerAndCreatorFields(t *testing.T) {
	b := store.Basket{ID: "pub", CreatorID: "user-uuid", Name: "Blue", Chain: "Base",
		IsPublic: true, FeeBps: 25, Subscribed: true,
		Weights: []store.Weight{{Asset: "USDC", WeightBps: 10000}}}
	f := &fakeBaskets{byID: map[string]store.Basket{"pub": b}, list: []store.Basket{b}}
	h := publicServer(f)

	for _, path := range []string{"/public/baskets/pub", "/public/baskets"} {
		t.Run(path, func(t *testing.T) {
			rec := get(t, h, path)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 without an Authorization header", rec.Code)
			}
			var any1 any
			if err := json.Unmarshal(rec.Body.Bytes(), &any1); err != nil {
				t.Fatal(err)
			}
			obj, ok := any1.(map[string]any)
			if !ok {
				arr := any1.([]any)
				if len(arr) != 1 {
					t.Fatalf("len = %d, want 1", len(arr))
				}
				obj = arr[0].(map[string]any)
			}
			for _, banned := range []string{"creator_id", "subscribed", "is_public"} {
				if _, present := obj[banned]; present {
					t.Errorf("response leaks %q: %s", banned, rec.Body.String())
				}
			}
			for _, want := range []string{"id", "name", "chain", "fee_bps", "weights", "created_at"} {
				if _, present := obj[want]; !present {
					t.Errorf("response is missing %q: %s", want, rec.Body.String())
				}
			}
		})
	}
}

func TestPublicListLimitIsClamped(t *testing.T) {
	tests := []struct {
		name, query string
		want        int
	}{
		{"absent", "", publicListDefault},
		{"garbage", "?limit=abc", publicListDefault},
		{"zero", "?limit=0", publicListDefault},
		{"negative", "?limit=-5", publicListDefault},
		{"in range", "?limit=10", 10},
		{"at ceiling", "?limit=100", publicListMax},
		{"over ceiling", "?limit=100000", publicListMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBaskets{}
			rec := get(t, publicServer(f), "/public/baskets"+tt.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if f.gotLimit != tt.want {
				t.Errorf("limit = %d, want %d", f.gotLimit, tt.want)
			}
			if !f.gotPublic {
				t.Error("publicOnly = false: the anonymous list must never include private baskets")
			}
		})
	}
}

// An empty list must marshal as [], not null, or the UI has to special-case it.
func TestPublicListEmptyIsArray(t *testing.T) {
	rec := get(t, publicServer(&fakeBaskets{}), "/public/baskets")
	if got := rec.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want %q", got, "[]\n")
	}
}

// A basket you made but have not joined must not read as "not joined": the two
// flags are independent, and the client had no signal for the first one because
// creator_id is (correctly) not exposed publicly.
func TestCreatedByMeIsIndependentOfSubscribed(t *testing.T) {
	mine := store.Basket{ID: "b1", CreatorID: "me"}
	tests := []struct {
		name                    string
		caller                  string
		subscribed              bool
		wantCreated, wantSubbed bool
	}{
		{"creator who has not joined", "me", false, true, false},
		{"creator who also joined", "me", true, true, true},
		{"someone else's basket", "other", false, false, false},
		{"someone else's basket, joined", "other", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ownerView(mine, tt.caller, tt.subscribed)
			if got.CreatedByMe != tt.wantCreated || got.Subscribed != tt.wantSubbed {
				t.Fatalf("created_by_me=%v subscribed=%v, want %v/%v",
					got.CreatedByMe, got.Subscribed, tt.wantCreated, tt.wantSubbed)
			}
		})
	}
}

// The public view has no caller, so both per-caller flags are meaningless there
// — and creator_id stays out of it entirely.
func TestPublicViewCarriesNoCallerFields(t *testing.T) {
	f := &fakeBaskets{byID: map[string]store.Basket{
		"pub": {ID: "pub", CreatorID: "user-uuid", Name: "Blue", IsPublic: true},
	}}
	body := get(t, publicServer(f), "/public/baskets/pub").Body.String()
	for _, absent := range []string{"created_by_me", "subscribed", "creator_id"} {
		if strings.Contains(body, absent) {
			t.Fatalf("%s must not appear in the public view: %s", absent, body)
		}
	}
}
