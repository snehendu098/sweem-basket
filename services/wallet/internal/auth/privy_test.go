package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testKeyPEM generates a real P-256 SPKI PEM, so the parse path is exercised
// against a genuine key rather than a fixture that only looks like one.
func testKeyPEM(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// stripNewlines produces the shape Privy's API actually returns: one unbroken
// line with the header and footer glued to the body.
func stripNewlines(p string) string { return strings.ReplaceAll(strings.TrimSpace(p), "\n", "") }

func TestNewVerifierAcceptsBothPEMShapes(t *testing.T) {
	valid := testKeyPEM(t)
	tests := []struct {
		name    string
		key     string
		appID   string
		wantErr bool
	}{
		{name: "PEM with newlines, as pasted from the dashboard", key: valid, appID: "app1"},
		{name: "PEM without newlines, as Privy's API returns it", key: stripNewlines(valid), appID: "app1"},
		{name: "PEM with surrounding whitespace", key: "\n  " + valid + "  \n", appID: "app1"},
		{name: "empty key is refused", key: "", appID: "app1", wantErr: true},
		{name: "empty app id is refused", key: valid, appID: "", wantErr: true},
		{name: "garbage is refused, not ignored", key: "-----BEGIN PUBLIC KEY-----nope-----END PUBLIC KEY-----", appID: "app1", wantErr: true},
		{name: "an RSA-looking blob is refused", key: "not a pem at all", appID: "app1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := NewVerifier(tt.key, tt.appID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("got a verifier, want an error — the service must not start unable to verify tokens")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.key == nil {
				t.Error("verifier has no key")
			}
		})
	}
}

// stubDoer answers without a network.
type stubDoer struct {
	status int
	body   string
	err    error
	req    *http.Request
	calls  int
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	return &http.Response{
		StatusCode: s.status,
		Body:       io.NopCloser(strings.NewReader(s.body)),
	}, nil
}

func TestFetchVerificationKey(t *testing.T) {
	const key = "-----BEGIN PUBLIC KEY-----MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE-----END PUBLIC KEY-----"

	tests := []struct {
		name      string
		doer      *stubDoer
		appID     string
		secret    string
		want      string
		wantErr   bool
		wantCalls int
	}{
		{
			name:  "200 with a key",
			doer:  &stubDoer{status: 200, body: `{"id":"app1","verification_key":"` + key + `"}`},
			appID: "app1", secret: "sec", want: key, wantCalls: 1,
		},
		{
			name:  "401 fails closed",
			doer:  &stubDoer{status: 401, body: `{"error":"bad credentials"}`},
			appID: "app1", secret: "sec", wantErr: true, wantCalls: 1,
		},
		{
			name:  "500 fails closed",
			doer:  &stubDoer{status: 500, body: `{}`},
			appID: "app1", secret: "sec", wantErr: true, wantCalls: 1,
		},
		{
			name:  "transport failure fails closed",
			doer:  &stubDoer{err: errors.New("dial tcp: no route to host")},
			appID: "app1", secret: "sec", wantErr: true, wantCalls: 1,
		},
		{
			name:  "200 with no key is still a failure",
			doer:  &stubDoer{status: 200, body: `{"id":"app1"}`},
			appID: "app1", secret: "sec", wantErr: true, wantCalls: 1,
		},
		{
			name:  "unparseable body fails closed",
			doer:  &stubDoer{status: 200, body: `<html>maintenance</html>`},
			appID: "app1", secret: "sec", wantErr: true, wantCalls: 1,
		},
		{
			// No credentials means no request to make.
			name:  "missing secret makes no call",
			doer:  &stubDoer{status: 200, body: `{"verification_key":"x"}`},
			appID: "app1", secret: "", wantErr: true, wantCalls: 0,
		},
		{
			name:  "missing app id makes no call",
			doer:  &stubDoer{status: 200, body: `{"verification_key":"x"}`},
			appID: "", secret: "sec", wantErr: true, wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FetchVerificationKey(context.Background(), tt.doer, tt.appID, tt.secret)
			if tt.wantErr {
				if err == nil {
					t.Fatal("got no error; the service would start unable to verify tokens")
				}
				// The secret must never travel in an error string.
				if tt.secret != "" && strings.Contains(err.Error(), tt.secret) {
					t.Error("error message leaks the app secret")
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("key = %q, want %q", got, tt.want)
			}
			if tt.doer.calls != tt.wantCalls {
				t.Errorf("made %d requests, want %d", tt.doer.calls, tt.wantCalls)
			}
		})
	}
}

// The credentials go in the Basic auth header and the app-id header, matching
// what api.privy.io expects — and nowhere else, such as the URL.
func TestFetchVerificationKeySendsCredentialsCorrectly(t *testing.T) {
	d := &stubDoer{status: 200, body: `{"verification_key":"k"}`}
	if _, err := FetchVerificationKey(context.Background(), d, "app1", "s3cret"); err != nil {
		t.Fatal(err)
	}
	user, pass, ok := d.req.BasicAuth()
	if !ok || user != "app1" || pass != "s3cret" {
		t.Errorf("basic auth = (%q, %q, %v), want (app1, s3cret, true)", user, pass, ok)
	}
	if got := d.req.Header.Get("privy-app-id"); got != "app1" {
		t.Errorf("privy-app-id = %q, want app1", got)
	}
	if want := "https://api.privy.io/v1/apps/app1"; d.req.URL.String() != want {
		t.Errorf("url = %q, want %q", d.req.URL, want)
	}
	if strings.Contains(d.req.URL.String(), "s3cret") {
		t.Error("app secret leaked into the URL")
	}
}

// The override path must not touch the network at all.
func TestOverrideSkipsTheFetch(t *testing.T) {
	v, err := NewVerifier(stripNewlines(testKeyPEM(t)), "app1")
	if err != nil {
		t.Fatalf("override key rejected: %v", err)
	}
	if v.appID != "app1" {
		t.Errorf("appID = %q, want app1", v.appID)
	}
}

// --- keeper shared-secret path ---

func keeperReq(secret, did string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/v1/baskets/b1/rebalance", nil)
	if secret != "" {
		r.Header.Set("X-Keeper-Secret", secret)
	}
	if did != "" {
		r.Header.Set("X-Acting-User", did)
	}
	return r
}

func TestKeeperAuthCaller(t *testing.T) {
	const secret = "s3cret-keeper-value"
	tests := []struct {
		name       string
		configured string
		sent       string
		did        string
		wantOK     bool
	}{
		{
			// The classic way this becomes an open door: unset secret must
			// disable the path, never wave requests through.
			name: "empty configured secret disables the path", configured: "", sent: "", did: "did:privy:abc",
		},
		{
			name:       "empty configured secret ignores even a matching empty header",
			configured: "", sent: "", did: "did:privy:abc",
		},
		{
			name:       "empty configured secret refuses any presented secret",
			configured: "", sent: "anything", did: "did:privy:abc",
		},
		{name: "wrong secret", configured: secret, sent: "wrong", did: "did:privy:abc"},
		{name: "no secret header", configured: secret, sent: "", did: "did:privy:abc"},
		{name: "secret prefix is not enough", configured: secret, sent: secret[:5], did: "did:privy:abc"},
		{name: "missing acting user", configured: secret, sent: secret, did: ""},
		{name: "acting user is not a privy did", configured: secret, sent: secret, did: "alice"},
		{name: "acting user is a bare prefix", configured: secret, sent: secret, did: "did:privy:"},
		{name: "acting user from another issuer", configured: secret, sent: secret, did: "did:ethr:abc"},
		{name: "correct secret and did", configured: secret, sent: secret, did: "did:privy:abc", wantOK: true},
		{name: "acting user is trimmed", configured: secret, sent: secret, did: "  did:privy:abc  ", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, ok := KeeperAuth{Secret: tt.configured}.Caller(keeperReq(tt.sent, tt.did))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				if c.DID != "" || c.ViaKeeper {
					t.Errorf("rejected request still produced a caller: %+v", c)
				}
				return
			}
			if c.DID != "did:privy:abc" {
				t.Errorf("DID = %q, want did:privy:abc", c.DID)
			}
			if !c.ViaKeeper {
				t.Error("ViaKeeper = false; the audit trail could not tell this was automated")
			}
		})
	}
}

// The zero value is what an unset KEEPER_SECRET produces, and it must be inert.
func TestZeroKeeperAuthIsDisabled(t *testing.T) {
	if _, ok := (KeeperAuth{}).Caller(keeperReq("", "did:privy:abc")); ok {
		t.Fatal("zero KeeperAuth authenticated a request")
	}
}

func TestSecretEqual(t *testing.T) {
	tests := []struct {
		name       string
		got, want  string
		wantResult bool
	}{
		{name: "equal", got: "abc", want: "abc", wantResult: true},
		{name: "different", got: "abc", want: "xyz"},
		{name: "shorter, a length mismatch must still compare", got: "ab", want: "abc"},
		{name: "longer", got: "abcd", want: "abc"},
		{name: "both empty compare equal; callers must gate on that separately", got: "", want: "", wantResult: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := secretEqual(tt.got, tt.want); got != tt.wantResult {
				t.Errorf("secretEqual = %v, want %v", got, tt.wantResult)
			}
		})
	}
}

// The keeper path must not weaken the token path on the same handler.
func TestMiddlewareAllowingKeeper(t *testing.T) {
	const secret = "s3cret-keeper-value"
	v := &Verifier{appID: "app1"} // no key: any token fails, which is the point

	tests := []struct {
		name       string
		keeper     KeeperAuth
		secret     string
		did        string
		wantStatus int
		wantKeeper bool
	}{
		{
			name: "valid keeper credentials pass", keeper: KeeperAuth{Secret: secret},
			secret: secret, did: "did:privy:abc", wantStatus: http.StatusOK, wantKeeper: true,
		},
		{
			name:   "wrong secret falls through to the token path and is refused",
			keeper: KeeperAuth{Secret: secret}, secret: "nope", did: "did:privy:abc",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:   "unset secret refuses even a well-formed keeper request",
			keeper: KeeperAuth{}, secret: secret, did: "did:privy:abc",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "no credentials at all", keeper: KeeperAuth{Secret: secret},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Caller
			h := v.MiddlewareAllowingKeeper(tt.keeper, http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					got, _ = FromContext(r.Context())
					w.WriteHeader(http.StatusOK)
				}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, keeperReq(tt.secret, tt.did))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if got.ViaKeeper != tt.wantKeeper {
				t.Errorf("ViaKeeper = %v, want %v", got.ViaKeeper, tt.wantKeeper)
			}
		})
	}
}

// Plain Middleware is token-only: a valid keeper secret must not open it.
func TestPlainMiddlewareIgnoresKeeperHeaders(t *testing.T) {
	v := &Verifier{appID: "app1"}
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("token-only middleware admitted a keeper request")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, keeperReq("any-secret", "did:privy:abc"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
