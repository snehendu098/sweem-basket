// Command sync-policy generates a Privy wallet policy from the executor's
// allowlists and creates or updates it on Privy.
//
// Why it exists: today the venue allowlist is enforced only by the executor's
// own code (executor/venues.json, executor/swaps.json). A compromised or
// misconfigured executor would face no second limit. A Privy policy moves the
// same constraint into Privy's enclave, so the delegated signer can only call
// contracts and methods the policy permits regardless of what our code asks for.
//
// The tool is a dry run by default. Pass -apply to actually write; that creates
// durable state on the Privy account and should never happen by accident.
//
//	go run ./services/wallet/cmd/sync-policy                      # print JSON
//	go run ./services/wallet/cmd/sync-policy -apply               # create
//	go run ./services/wallet/cmd/sync-policy -apply -policy-id X  # update in place
//
// Credentials come from the repo-root .env (PRIVY_APP_ID, PRIVY_APP_SECRET,
// PRIVY_AUTHORIZATION_PRIVATE_KEY, PRIVY_KEY_QUORUM_ID) and are never printed.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const privyAPI = "https://api.privy.io"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sync-policy:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		venuesPath = flag.String("venues", "executor/venues.json", "path to the venue allowlist")
		swapsPath  = flag.String("swaps", "executor/swaps.json", "path to the swap allowlist")
		envPath    = flag.String("env", ".env", "dotenv file to read credentials from if they are not already in the environment")
		name       = flag.String("name", "basket executor allowlist", "policy name")
		policyID   = flag.String("policy-id", "", "existing policy to update in place; empty creates a new one")
		out        = flag.String("out", "", "optional file to write the resulting policy id to")
		apply      = flag.Bool("apply", false, "actually create or update the policy on Privy. Without this the tool prints the document and exits")
		dryRun     = flag.Bool("dry-run", false, "force a dry run even if -apply is passed")
	)
	flag.Parse()

	venues, swaps, err := loadAllowlists(*venuesPath, *swapsPath)
	if err != nil {
		return err
	}

	write := *apply && !*dryRun

	// Fall back to PRIVY_POLICY_ID so a re-sync after regenerating the allowlist
	// is `-apply` alone. Without it the tool would create a second policy rather
	// than update the one that is attached, and the attached one would go stale.
	if *policyID == "" {
		loadDotenv(*envPath)
		*policyID = os.Getenv("PRIVY_POLICY_ID")
	}
	// The owner is only sent on create. Reading it for a dry run would fail on a
	// machine without credentials, which would make the dry run useless.
	ownerID := ""
	if write && *policyID == "" {
		loadDotenv(*envPath)
		if ownerID, err = mustEnv("PRIVY_KEY_QUORUM_ID"); err != nil {
			return err
		}
	}

	policy, err := Build(*name, ownerID, venues, swaps)
	if err != nil {
		return err
	}

	doc, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(doc))

	if !write {
		fmt.Fprintln(os.Stderr, "\ndry run: nothing was sent to Privy. Re-run with -apply to write.")
		return nil
	}

	loadDotenv(*envPath)
	c, err := newClient()
	if err != nil {
		return err
	}

	id := *policyID
	if id == "" {
		created, err := c.createPolicy(policy)
		if err != nil {
			return err
		}
		id = created
		fmt.Fprintf(os.Stderr, "\ncreated policy %s\n", id)
	} else {
		// PATCH replaces the rule list wholesale, which is what keeps Privy in
		// sync after venues.json is regenerated. owner_id is immutable and is
		// left out of the body.
		if err := c.updatePolicy(id, policy); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\nupdated policy %s\n", id)
	}

	// Read it back rather than trusting the write response.
	got, err := c.getPolicy(id)
	if err != nil {
		return fmt.Errorf("readback: %w", err)
	}
	fmt.Fprintf(os.Stderr, "readback: %d rules, owner_id %s\n", len(got.Rules), orNone(got.OwnerID))

	if *out != "" {
		if err := os.WriteFile(*out, []byte(id+"\n"), 0o644); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, `
The policy exists but enforces nothing until it is attached to a signer.
Attachment happens when the user delegates: client/src/lib/session.tsx calls
addSigners({signers: [{signerId, policyIds}]}). Set this build-time var and
rebuild the client, then have each user re-delegate:

    NEXT_PUBLIC_PRIVY_POLICY_ID=%s

Users who delegated before this is set keep an unrestricted signer until they
re-delegate.
`, id)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func loadAllowlists(venuesPath, swapsPath string) (venueFile, swapFile, error) {
	var v venueFile
	var s swapFile
	if err := readJSON(venuesPath, &v); err != nil {
		return v, s, err
	}
	if err := readJSON(swapsPath, &s); err != nil {
		return v, s, err
	}
	return v, s, nil
}

func readJSON(path string, into any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w (run from the repo root, or pass -venues/-swaps)", path, err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// loadDotenv fills in vars that are not already set. Best effort: a missing file
// is fine when the environment already carries the credentials.
func loadDotenv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.TrimSpace(v)
		v = strings.TrimSuffix(strings.TrimPrefix(v, `"`), `"`)
		v = strings.TrimSuffix(strings.TrimPrefix(v, `'`), `'`)
		if _, set := os.LookupEnv(k); !set && v != "" {
			os.Setenv(k, v)
		}
	}
}

func mustEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s is not set", key)
	}
	return v, nil
}

// ------------------------------------------------------------- Privy client

type client struct {
	appID  string
	secret string
	key    *ecdsa.PrivateKey
	http   *http.Client
}

func newClient() (*client, error) {
	appID, err := mustEnv("PRIVY_APP_ID")
	if err != nil {
		return nil, err
	}
	secret, err := mustEnv("PRIVY_APP_SECRET")
	if err != nil {
		return nil, err
	}
	raw, err := mustEnv("PRIVY_AUTHORIZATION_PRIVATE_KEY")
	if err != nil {
		return nil, err
	}
	key, err := parseAuthKey(raw)
	if err != nil {
		return nil, err
	}
	return &client{appID: appID, secret: secret, key: key, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// parseAuthKey reads a `wallet-auth:<base64 PKCS#8 P-256 key>` credential.
func parseAuthKey(raw string) (*ecdsa.PrivateKey, error) {
	b64 := strings.TrimPrefix(strings.TrimSpace(raw), "wallet-auth:")
	der, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("PRIVY_AUTHORIZATION_PRIVATE_KEY is not valid base64")
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("PRIVY_AUTHORIZATION_PRIVATE_KEY is not a PKCS#8 key")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PRIVY_AUTHORIZATION_PRIVATE_KEY is not an ECDSA key")
	}
	return ec, nil
}

func (c *client) createPolicy(p Policy) (string, error) {
	var got Policy
	if err := c.do("POST", privyAPI+"/v1/policies", p, &got); err != nil {
		return "", err
	}
	if got.ID == "" {
		return "", fmt.Errorf("create succeeded but returned no policy id")
	}
	return got.ID, nil
}

func (c *client) updatePolicy(id string, p Policy) error {
	body := struct {
		Name  string `json:"name"`
		Rules []Rule `json:"rules"`
	}{p.Name, p.Rules}
	return c.do("PATCH", privyAPI+"/v1/policies/"+id, body, nil)
}

func (c *client) getPolicy(id string) (Policy, error) {
	var got Policy
	err := c.do("GET", privyAPI+"/v1/policies/"+id, nil, &got)
	return got, err
}

func (c *client) do(method, url string, body, into any) error {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = b
	}

	req, err := http.NewRequest(method, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.appID, c.secret)
	req.Header.Set("privy-app-id", c.appID)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
		sig, err := c.sign(method, url, payload)
		if err != nil {
			return err
		}
		req.Header.Set("privy-authorization-signature", sig)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw)))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(raw, into)
}

// sign produces the privy-authorization-signature header: ECDSA P-256 over the
// RFC 8785 canonicalization of the request description. Cross-checked against
// executor/src/auth.rs, which is verified against @privy-io/node.
func (c *client) sign(method, url string, body []byte) (string, error) {
	payload, err := canonical(map[string]any{
		"version": 1,
		"method":  method,
		"url":     strings.TrimRight(url, "/"),
		"body":    json.RawMessage(body),
		"headers": map[string]any{"privy-app-id": c.appID},
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	der, err := ecdsa.SignASN1(rand.Reader, c.key, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// canonical renders v as RFC 8785 JSON: object keys sorted, no whitespace, no
// HTML escaping. Round-tripping through `any` is what sorts the nested objects;
// encoding/json only sorts maps, and UseNumber keeps numeric literals verbatim.
func canonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
