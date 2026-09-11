package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/snehendu098/sweem-basket/services/wallet/internal/auth"
)

const keeperSecret = "s3cret-keeper-value"

func testServer(t *testing.T) http.Handler {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	v, err := auth.NewVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), "app1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Auth:   v,
		Keeper: auth.KeeperAuth{Secret: keeperSecret},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return s.Routes()
}

// The keeper's secret must open exactly one door. Every other route stays
// token-only: a shared secret that reached /deposit would let whoever holds it
// move funds into arbitrary venues.
//
// The handlers here would nil-panic on a real Store, which is the point — a
// 401 proves the request was stopped at the middleware, before any handler.
func TestKeeperSecretIsRefusedOnEveryRouteButRebalance(t *testing.T) {
	h := testServer(t)

	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/baskets/b1/deposit"},
		{http.MethodGet, "/v1/me"},
		{http.MethodPost, "/v1/me"},
		{http.MethodPost, "/v1/baskets"},
		{http.MethodGet, "/v1/baskets"},
		{http.MethodGet, "/v1/baskets/b1"},
		{http.MethodPost, "/v1/baskets/b1/subscribe"},
		{http.MethodDelete, "/v1/baskets/b1/subscribe"},
		{http.MethodGet, "/v1/baskets/b1/plan"},
		{http.MethodGet, "/v1/portfolio"},
		{http.MethodGet, "/v1/executions"},
	}

	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			req.Header.Set("X-Keeper-Secret", keeperSecret)
			req.Header.Set("X-Acting-User", "did:privy:abc")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401: the keeper secret must not reach this route", rec.Code)
			}
		})
	}
}

// A rebalance without valid keeper credentials must still be refused, so the
// route's own token check is not weakened by mounting the keeper path on it.
func TestRebalanceStillRefusesBadCredentials(t *testing.T) {
	h := testServer(t)
	tests := []struct{ name, secret, did string }{
		{name: "no credentials at all"},
		{name: "wrong secret", secret: "wrong", did: "did:privy:abc"},
		{name: "valid secret, no acting user", secret: keeperSecret},
		{name: "valid secret, malformed acting user", secret: keeperSecret, did: "alice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/baskets/b1/rebalance", nil)
			if tt.secret != "" {
				req.Header.Set("X-Keeper-Secret", tt.secret)
			}
			if tt.did != "" {
				req.Header.Set("X-Acting-User", tt.did)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// With KEEPER_SECRET unset the path is disabled, not bypassed: the same
// request that would otherwise succeed is refused.
func TestEmptyKeeperSecretDisablesTheRoute(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	v, err := auth.NewVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), "app1")
	if err != nil {
		t.Fatal(err)
	}
	s.Auth = v // Keeper left as the zero value, as an unset KEEPER_SECRET gives

	req := httptest.NewRequest(http.MethodPost, "/v1/baskets/b1/rebalance", nil)
	req.Header.Set("X-Keeper-Secret", "")
	req.Header.Set("X-Acting-User", "did:privy:abc")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: an unset secret must disable the keeper path, not open it", rec.Code)
	}
}
