package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/snehendu098/sweem-basket/internal/shared/chains"
	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

const ChanUpdate = "venues:updated"

func KeyIndex(chain string) string { return "venues:index:" + normChain(chain) }

func KeyAPYKeys(chain string) string { return "apy:keys:" + normChain(chain) }

func KeyVenue(id string) string { return "venue:" + id }

func KeyAPY(chain, asset string) string {
	return fmt.Sprintf("apy:%s:%s", normChain(chain), strings.ToUpper(asset))
}

func normChain(chain string) string { return strings.ToLower(strings.TrimSpace(chain)) }

type Store struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb} }

type UpdateMessage struct {
	Chain string    `json:"chain"`
	Count int       `json:"count"`
	At    time.Time `json:"at"`
}

func (s *Store) Publish(ctx context.Context, chain string, venues []venue.Venue, ttl time.Duration) error {
	for _, v := range venues {
		if !strings.EqualFold(v.Chain, chain) {
			return fmt.Errorf("venue %s is on %s, refusing to publish it under %s", v.ID, v.Chain, chain)
		}
	}
	oldZSets, err := s.rdb.SMembers(ctx, KeyAPYKeys(chain)).Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read apy key set: %w", err)
	}

	pipe := s.rdb.TxPipeline()
	if len(oldZSets) > 0 {
		pipe.Del(ctx, oldZSets...)
	}
	pipe.Del(ctx, KeyIndex(chain), KeyAPYKeys(chain))

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
		pipe.SAdd(ctx, KeyAPYKeys(chain), keys...)
		pipe.Expire(ctx, KeyAPYKeys(chain), ttl)
	}
	if len(ids) > 0 {
		pipe.SAdd(ctx, KeyIndex(chain), ids...)
		pipe.Expire(ctx, KeyIndex(chain), ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("exec venue pipeline: %w", err)
	}

	payload, _ := json.Marshal(UpdateMessage{Chain: chain, Count: len(venues), At: time.Now().UTC()})
	return s.rdb.Publish(ctx, ChanUpdate, payload).Err()
}

type Query struct {
	Chain  string
	Asset  string
	MinTVL float64
	Limit  int
}

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

func (s *Store) ids(ctx context.Context, q Query) ([]string, error) {
	if q.Chain != "" && q.Asset != "" {
		return s.rdb.ZRevRange(ctx, KeyAPY(q.Chain, q.Asset), 0, -1).Result()
	}
	if q.Chain != "" {
		return s.rdb.SMembers(ctx, KeyIndex(q.Chain)).Result()
	}
	var out []string
	for _, id := range chains.Supported() {
		label, _ := chains.Label(id)
		ids, err := s.rdb.SMembers(ctx, KeyIndex(label)).Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	return out, nil
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
		if err != nil || len(m) == 0 {
			continue
		}
		out = append(out, venue.FromMap(m))
	}
	return out, nil
}

func (s *Store) Count(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	for _, id := range chains.Supported() {
		label, _ := chains.Label(id)
		n, err := s.rdb.SCard(ctx, KeyIndex(label)).Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		out[label] = int(n)
	}
	return out, nil
}

type AssetSummary struct {
	Asset       string  `json:"asset"`
	Chain       string  `json:"chain"`
	Venues      int     `json:"venues"`
	BestAPY     float64 `json:"best_apy"`
	BestVenue   string  `json:"best_venue"`
	TotalTVLUsd float64 `json:"total_tvl_usd"`
}

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

const KeySources = "venues:sources"

func (s *Store) PublishSources(ctx context.Context, statuses any, ttl time.Duration) error {
	payload, err := json.Marshal(statuses)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, KeySources, payload, ttl).Err()
}
