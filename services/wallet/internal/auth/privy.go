// Package auth verifies Privy access tokens and puts the caller on the context.
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey struct{}

// Caller is the authenticated principal behind a request.
type Caller struct {
	DID string // Privy user DID, e.g. "did:privy:abc123"
	// ViaKeeper marks a request authenticated by the keeper's shared secret
	// rather than a user's own token, so the audit trail can tell an automated
	// move from one the user asked for.
	ViaKeeper bool
}

// KeeperAuth lets the keeper act for a user without a Privy token: the keeper
// cannot mint one. An empty Secret disables the path entirely — it is never a
// bypass, and the zero value is therefore safe.
type KeeperAuth struct {
	Secret string // KEEPER_SECRET
}

// Caller authenticates a keeper request. It answers false for anything it is
// not certain about, so the caller falls through to the normal token check.
func (k KeeperAuth) Caller(r *http.Request) (Caller, bool) {
	if k.Secret == "" {
		return Caller{}, false
	}
	if !secretEqual(r.Header.Get("X-Keeper-Secret"), k.Secret) {
		return Caller{}, false
	}
	// The keeper names the user it acts for. It is a trigger, not an
	// authorization: the handler still checks delegation for that user.
	did := strings.TrimSpace(r.Header.Get("X-Acting-User"))
	if !strings.HasPrefix(did, "did:privy:") || len(did) <= len("did:privy:") {
		return Caller{}, false
	}
	return Caller{DID: did, ViaKeeper: true}, true
}

// secretEqual compares in constant time without leaking the secret's length.
// subtle.ConstantTimeCompare short-circuits to 0 on a length mismatch, which
// makes the comparison time reveal how long the real secret is; hashing both
// sides first makes every comparison a fixed 32 bytes.
func secretEqual(got, want string) bool {
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// Verifier validates Privy access tokens. Privy signs them ES256; the public
// key (SPKI PEM) is fetched from Privy's API at startup, or supplied directly
// via PRIVY_VERIFICATION_KEY.
type Verifier struct {
	key   *ecdsa.PublicKey
	appID string
}

// NewVerifier parses an SPKI PEM verification key. appID is the Privy app ID,
// checked as the token audience.
func NewVerifier(pemKey, appID string) (*Verifier, error) {
	if strings.TrimSpace(pemKey) == "" {
		return nil, errors.New("auth: empty verification key")
	}
	if strings.TrimSpace(appID) == "" {
		return nil, errors.New("auth: empty app id")
	}
	pub, err := jwt.ParseECPublicKeyFromPEM(normalizePEM(pemKey))
	if err != nil {
		return nil, fmt.Errorf("auth: parse verification key: %w", err)
	}
	return &Verifier{key: pub, appID: appID}, nil
}

// normalizePEM rewraps a PEM whose newlines were stripped.
//
// Privy's API returns the key as one unbroken line —
// "-----BEGIN PUBLIC KEY-----MFkw…QQ==-----END PUBLIC KEY-----" — which
// encoding/pem will not parse. A key pasted from the dashboard already has its
// newlines, so this is a no-op for the override path.
func normalizePEM(key string) []byte {
	key = strings.TrimSpace(key)
	if strings.Contains(key, "\n") {
		return []byte(key)
	}
	const begin, end = "-----BEGIN PUBLIC KEY-----", "-----END PUBLIC KEY-----"
	body, ok := strings.CutPrefix(key, begin)
	if !ok {
		return []byte(key) // not a shape we recognise; let the parser complain
	}
	body, ok = strings.CutSuffix(body, end)
	if !ok {
		return []byte(key)
	}
	var b strings.Builder
	b.WriteString(begin + "\n")
	for body = strings.TrimSpace(body); len(body) > 64; body = body[64:] {
		b.WriteString(body[:64] + "\n")
	}
	b.WriteString(body + "\n" + end + "\n")
	return []byte(b.String())
}

// Doer is the HTTP client used to fetch the verification key, injected so
// tests never reach the network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// FetchVerificationKey reads the app's ES256 verification key from Privy's API.
//
// One call at startup, cached for the process lifetime by the caller — the key
// does not rotate mid-run, and a per-request fetch would put Privy on the
// critical path of every authenticated call.
//
// Credentials go in the Basic auth header and appear in no log line or error
// message here; callers must keep it that way.
func FetchVerificationKey(ctx context.Context, http_ Doer, appID, appSecret string) (string, error) {
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(appSecret) == "" {
		return "", errors.New("auth: PRIVY_APP_ID and PRIVY_APP_SECRET are required to fetch the verification key")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.privy.io/v1/apps/"+appID, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(appID, appSecret)
	req.Header.Set("privy-app-id", appID)
	req.Header.Set("Accept", "application/json")

	resp, err := http_.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: fetch verification key: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Status only. The body can echo request details, and the credentials
		// must never reach a log.
		return "", fmt.Errorf("auth: fetch verification key: status %d", resp.StatusCode)
	}
	var out struct {
		VerificationKey string `json:"verification_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("auth: decode app response: %w", err)
	}
	if strings.TrimSpace(out.VerificationKey) == "" {
		return "", errors.New("auth: privy returned no verification key")
	}
	return out.VerificationKey, nil
}

// Verify checks signature, algorithm, issuer, audience and expiry.
func (v *Verifier) Verify(token string) (Caller, error) {
	claims := jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, &claims,
		func(t *jwt.Token) (any, error) { return v.key, nil },
		// Pinning ES256 is what stops an alg-confusion downgrade.
		jwt.WithValidMethods([]string{"ES256"}),
		jwt.WithIssuer("privy.io"),
		jwt.WithAudience(v.appID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return Caller{}, fmt.Errorf("auth: invalid token: %w", err)
	}
	if claims.Subject == "" {
		return Caller{}, errors.New("auth: token has no subject")
	}
	return Caller{DID: claims.Subject}, nil
}

// Middleware rejects unauthenticated requests and stores the Caller on the
// context. Privy tokens only.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	// The zero KeeperAuth has an empty secret, so the keeper path is off.
	return v.MiddlewareAllowingKeeper(KeeperAuth{}, next)
}

// MiddlewareAllowingKeeper also accepts the keeper's shared secret. Mount it on
// the single route the keeper is allowed to reach, never on the whole API: a
// secret that reached /deposit would let whoever holds it move funds into
// arbitrary venues.
func (v *Verifier) MiddlewareAllowingKeeper(k KeeperAuth, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if caller, ok := k.Caller(r); ok {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, caller)))
			return
		}
		raw := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(raw, "Bearer ")
		if !ok || token == "" {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		caller, err := v.Verify(token)
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, caller)))
	})
}

// FromContext returns the authenticated caller, if any.
func FromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}
