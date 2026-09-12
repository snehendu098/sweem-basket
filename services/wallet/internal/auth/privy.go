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

type Caller struct {
	DID       string
	ViaKeeper bool
}

type KeeperAuth struct {
	Secret string
}

func (k KeeperAuth) Caller(r *http.Request) (Caller, bool) {
	if k.Secret == "" {
		return Caller{}, false
	}
	if !secretEqual(r.Header.Get("X-Keeper-Secret"), k.Secret) {
		return Caller{}, false
	}
	did := strings.TrimSpace(r.Header.Get("X-Acting-User"))
	if !strings.HasPrefix(did, "did:privy:") || len(did) <= len("did:privy:") {
		return Caller{}, false
	}
	return Caller{DID: did, ViaKeeper: true}, true
}

func secretEqual(got, want string) bool {
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

type Verifier struct {
	key   *ecdsa.PublicKey
	appID string
}

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

func normalizePEM(key string) []byte {
	key = strings.TrimSpace(key)
	if strings.Contains(key, "\n") {
		return []byte(key)
	}
	const begin, end = "-----BEGIN PUBLIC KEY-----", "-----END PUBLIC KEY-----"
	body, ok := strings.CutPrefix(key, begin)
	if !ok {
		return []byte(key)
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

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

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

func (v *Verifier) Verify(token string) (Caller, error) {
	claims := jwt.RegisteredClaims{}
	_, err := jwt.ParseWithClaims(token, &claims,
		func(t *jwt.Token) (any, error) { return v.key, nil },
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

func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return v.MiddlewareAllowingKeeper(KeeperAuth{}, next)
}

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

func FromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(ctxKey{}).(Caller)
	return c, ok
}
