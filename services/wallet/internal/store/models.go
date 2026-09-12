package store

import (
	"encoding/json"
	"time"
)

type User struct {
	ID            string    `json:"id"`
	PrivyDID      string    `json:"privy_did"`
	WalletAddress string    `json:"wallet_address"`
	PrivyWalletID string    `json:"privy_wallet_id"`
	Delegated     bool      `json:"delegated"`
	CreatedAt     time.Time `json:"created_at"`
}

type Weight struct {
	Asset     string `json:"asset"`
	WeightBps int    `json:"weight_bps"`
}

type Basket struct {
	ID          string    `json:"id"`
	CreatorID   string    `json:"creator_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Chain       string    `json:"chain"`
	IsPublic    bool      `json:"is_public"`
	FeeBps      int       `json:"fee_bps"`
	Weights     []Weight  `json:"weights"`
	Subscribed  bool      `json:"subscribed"`
	CreatedByMe bool      `json:"created_by_me"`
	CreatedAt   time.Time `json:"created_at"`
}

type Position struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	BasketID  string    `json:"basket_id"`
	Asset     string    `json:"asset"`
	VenueID   string    `json:"venue_id"`
	Chain     string    `json:"chain"`
	Project   string    `json:"project"`
	AmountUSD float64   `json:"amount_usd"`
	EntryAPY  float64   `json:"entry_apy"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Execution struct {
	ID        string          `json:"id"`
	UserID    string          `json:"user_id"`
	BasketID  *string         `json:"basket_id,omitempty"`
	Kind      string          `json:"kind"`
	Asset     string          `json:"asset"`
	FromVenue *string         `json:"from_venue,omitempty"`
	ToVenue   *string         `json:"to_venue,omitempty"`
	AmountUSD float64         `json:"amount_usd"`
	TxHash    *string         `json:"tx_hash,omitempty"`
	Status    string          `json:"status"`
	Error     *string         `json:"error,omitempty"`
	Steps     json.RawMessage `json:"steps,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Subscription struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	BasketID  string    `json:"basket_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}
