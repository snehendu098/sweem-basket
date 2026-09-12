package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/snehendu098/sweem-basket/services/market-data/internal/venue"
)

const DefaultGatewayURL = "https://gateway.thegraph.com/api/subgraphs/id"

type ProtocolAdapter interface {
	Protocol() string
	SubgraphID() string
	Query() string
	Map(p *Pricer, raw json.RawMessage) ([]venue.Venue, error)
}

type GraphSource struct {
	Chain    Chain
	Gateway  string
	APIKey   string
	Client   *http.Client
	Filter   Filter
	Prices   PriceFeed
	Adapters []ProtocolAdapter

	mu     sync.RWMutex
	status map[string]Status
}

func NewGraph(chain Chain, gateway, apiKey string, f Filter, feed PriceFeed, adapters ...ProtocolAdapter) (*GraphSource, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("GRAPH_API_KEY is required: subgraph queries cannot be authenticated without it")
	}
	if feed == nil {
		return nil, errors.New("price feed is required: venue TVL cannot be valued without it")
	}
	if len(adapters) == 0 {
		return nil, errors.New("no protocol adapters registered")
	}
	if gateway == "" {
		gateway = DefaultGatewayURL
	}
	return &GraphSource{
		Chain:    chain,
		Gateway:  strings.TrimRight(gateway, "/"),
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: 30 * time.Second},
		Filter:   f,
		Prices:   feed,
		Adapters: adapters,
		status:   map[string]Status{},
	}, nil
}

func (g *GraphSource) Name() string { return "thegraph:" + g.Chain.Label }

func (g *GraphSource) Fetch(ctx context.Context) ([]venue.Venue, error) {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []venue.Venue
	)
	for _, a := range g.Adapters {
		wg.Add(1)
		go func(a ProtocolAdapter) {
			defer wg.Done()
			st := Status{
				Protocol: a.Protocol(), SubgraphID: a.SubgraphID(), Source: g.Name(),
				Chain: g.Chain.Label, ChainID: g.Chain.ID,
				LastAttempt: time.Now().UTC(),
			}

			p := NewPricer(ctx, g.Prices)
			venues, err := g.query(ctx, a, p)
			if err != nil {
				st.Error = err.Error()
				slog.Error("subgraph query failed", "protocol", a.Protocol(), "chain", g.Chain.Label, "err", err)
				g.setStatus(st, false)
				return
			}

			kept := venues[:0]
			for i := range venues {
				if g.Filter.Screen(&venues[i]) {
					kept = append(kept, venues[i])
				}
			}
			st.OK, st.Venues, st.LastSuccess = true, len(kept), st.LastAttempt
			st.Unpriceable = p.Unpriceable()
			g.setStatus(st, true)
			slog.Info("subgraph query ok", "protocol", a.Protocol(), "chain", g.Chain.Label,
				"mapped", len(venues), "kept", len(kept))

			mu.Lock()
			out = append(out, kept...)
			mu.Unlock()
		}(a)
	}
	wg.Wait()

	out = StackIntrinsic(out)

	if len(out) == 0 && !g.anyOK() {
		return nil, fmt.Errorf("all %d subgraph adapters failed on %s", len(g.Adapters), g.Chain.Label)
	}
	return out, nil
}

func (g *GraphSource) Status() []Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]Status, 0, len(g.Adapters))
	for _, a := range g.Adapters {
		st, ok := g.status[a.Protocol()]
		if !ok {
			st = Status{
				Protocol: a.Protocol(), SubgraphID: a.SubgraphID(), Source: g.Name(),
				Chain: g.Chain.Label, ChainID: g.Chain.ID,
			}
		}
		if st.SubgraphID == "" {
			st.Error = "no subgraph id configured for " + SubgraphEnvKey(protocolPrefix(a.Protocol()), g.Chain)
		}
		out = append(out, st)
	}
	return out
}

func (g *GraphSource) setStatus(st Status, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !ok {
		if prev, found := g.status[st.Protocol]; found {
			st.LastSuccess, st.Venues = prev.LastSuccess, prev.Venues
		}
	}
	g.status[st.Protocol] = st
}

func (g *GraphSource) anyOK() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, st := range g.status {
		if st.OK {
			return true
		}
	}
	return false
}

func SubgraphURL(gateway, subgraphID string) string {
	if strings.HasPrefix(subgraphID, "http") {
		return subgraphID
	}
	return strings.TrimRight(gateway, "/") + "/" + subgraphID
}

type graphResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (g *GraphSource) query(ctx context.Context, a ProtocolAdapter, p *Pricer) ([]venue.Venue, error) {
	if a.SubgraphID() == "" {
		return nil, errors.New("no subgraph id configured for " + SubgraphEnvKey(protocolPrefix(a.Protocol()), g.Chain))
	}
	body, err := json.Marshal(map[string]string{"query": a.Query()})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, SubgraphURL(g.Gateway, a.SubgraphID()), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.APIKey)

	resp, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned %d", resp.StatusCode)
	}

	var gr graphResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(gr.Errors) > 0 {
		return nil, fmt.Errorf("graphql error: %s", gr.Errors[0].Message)
	}
	return a.Map(p, gr.Data)
}

var nowUTC = func() time.Time { return time.Now().UTC() }

func protocolPrefix(protocol string) string {
	switch protocol {
	case "aave-v3":
		return "AAVE_V3"
	case "compound-v3":
		return "COMPOUND_V3"
	case "moonwell":
		return "MOONWELL"
	case "morpho-blue":
		return "MORPHO"
	case ProtocolHold:
		return "HOLD"
	default:
		return strings.ToUpper(strings.ReplaceAll(protocol, "-", "_"))
	}
}
