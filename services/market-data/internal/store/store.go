// Package store owns the Redis key schema for venues.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

const (
	KeyIndex   = "venues:index" // SET of live venue ids
	KeyAPYKeys = "apy:keys"     // SET of live apy:* zset keys (so we can rebuild them)
	ChanUpdate = "venues:updated"
)

func KeyVenue(id string) string { return "venue:" + id }

func KeyAPY(chain, asset string) string {
	return fmt.Sprintf("apy:%s:%s", chain, strings.ToUpper(asset))
}

type Store struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

// UpdateMessage is the pubsub payload published after each successful cycle.
type UpdateMessage struct {
	Count int       `json:"count"`
	At    time.Time `json:"at"`
}

// Publish rewrites the whole venue view in one transaction: hashes with a TTL,
// freshly rebuilt APY zsets, and the id index.
func (s *Store) Publish(ctx context.Context, venues []venue.Venue, ttl time.Duration) error {
	oldZSets, err := s.rdb.SMembers(ctx, KeyAPYKeys).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read apy key set: %w", err)
	}

	pipe := s.rdb.TxPipeline()
	if len(oldZSets) > 0 {
		pipe.Del(ctx, oldZSets...)
	}
	pipe.Del(ctx, KeyIndex, KeyAPYKeys)

	ids := make([]any, 0, len(venues))
	zsets := map[string]bool{}
	for _, v := range venues {
		key := KeyVenue(v.ID)
		pipe.HSet(ctx, key, v.Map())
		pipe.Expire(ctx, key, ttl)

		zk := KeyAPY(v.Chain, v.Asset)
		pipe.ZAdd(ctx, zk, redis.Z{Score: v.APY, Member: v.ID})
		if !zsets[zk] {
			zsets[zk] = true
			pipe.Expire(ctx, zk, ttl)
		}
		ids = append(ids, v.ID)
	}
	if len(zsets) > 0 {
		keys := make([]any, 0, len(zsets))
		for k := range zsets {
			keys = append(keys, k)
		}
		pipe.SAdd(ctx, KeyAPYKeys, keys...)
		pipe.Expire(ctx, KeyAPYKeys, ttl)
	}
	if len(ids) > 0 {
		pipe.SAdd(ctx, KeyIndex, ids...)
		pipe.Expire(ctx, KeyIndex, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("exec venue pipeline: %w", err)
	}

	payload, _ := json.Marshal(UpdateMessage{Count: len(venues), At: time.Now().UTC()})
	return s.rdb.Publish(ctx, ChanUpdate, payload).Err()
}

// Query filters the live venue set. Empty chain/asset means "any".
type Query struct {
	Chain  string
	Asset  string
	MinTVL float64
	Limit  int
}

// List returns venues matching q, sorted by APY descending.
func (s *Store) List(ctx context.Context, q Query) ([]venue.Venue, error) {
	ids, err := s.ids(ctx, q)
	if err != nil {
		return nil, err
	}
	venues, err := s.get(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := venues[:0]
	for _, v := range venues {
		if q.Chain != "" && !strings.EqualFold(v.Chain, q.Chain) {
			continue
		}
		if q.Asset != "" && !strings.EqualFold(v.Asset, q.Asset) {
			continue
		}
		if v.TVLUsd < q.MinTVL {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].APY > out[j].APY })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// ids picks the cheapest source of candidate ids: the APY zset when both chain
// and asset are pinned, the full index otherwise.
func (s *Store) ids(ctx context.Context, q Query) ([]string, error) {
	if q.Chain != "" && q.Asset != "" {
		return s.rdb.ZRevRange(ctx, KeyAPY(q.Chain, q.Asset), 0, -1).Result()
	}
	return s.rdb.SMembers(ctx, KeyIndex).Result()
}

func (s *Store) get(ctx context.Context, ids []string) ([]venue.Venue, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, KeyVenue(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("load venues: %w", err)
	}
	out := make([]venue.Venue, 0, len(ids))
	for _, c := range cmds {
		m, err := c.Result()
		if err != nil || len(m) == 0 { // expired between index read and fetch
			continue
		}
		out = append(out, venue.FromMap(m))
	}
	return out, nil
}

// Count returns the number of live venues.
func (s *Store) Count(ctx context.Context) (int, error) {
	n, err := s.rdb.SCard(ctx, KeyIndex).Result()
	return int(n), err
}

// AssetSummary powers the basket-builder UI.
type AssetSummary struct {
	Asset       string  `json:"asset"`
	Chain       string  `json:"chain"`
	Venues      int     `json:"venues"`
	BestAPY     float64 `json:"best_apy"`
	BestVenue   string  `json:"best_venue"`
	TotalTVLUsd float64 `json:"total_tvl_usd"`
}

// Assets aggregates live venues by (chain, asset).
func (s *Store) Assets(ctx context.Context, chain string) ([]AssetSummary, error) {
	venues, err := s.List(ctx, Query{Chain: chain})
	if err != nil {
		return nil, err
	}
	byAsset := map[string]*AssetSummary{}
	for _, v := range venues {
		k := v.Chain + ":" + v.Asset
		a, ok := byAsset[k]
		if !ok {
			a = &AssetSummary{Asset: v.Asset, Chain: v.Chain}
			byAsset[k] = a
		}
		a.Venues++
		a.TotalTVLUsd += v.TVLUsd
		if v.APY > a.BestAPY {
			a.BestAPY, a.BestVenue = v.APY, v.ID
		}
	}
	out := make([]AssetSummary, 0, len(byAsset))
	for _, a := range byAsset {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BestAPY > out[j].BestAPY })
	return out, nil
}

// KeySources holds the JSON-encoded per-protocol fetch status from the last cycle.
const KeySources = "venues:sources"

// PublishSources records adapter health for the API's /sources endpoint.
func (s *Store) PublishSources(ctx context.Context, statuses any, ttl time.Duration) error {
	payload, err := json.Marshal(statuses)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, KeySources, payload, ttl).Err()
}
