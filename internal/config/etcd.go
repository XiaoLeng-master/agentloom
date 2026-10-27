package config

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

const defaultEtcdPrefix = "/agentloom/agents/"

// EtcdOptions selects an optional etcd config source.
type EtcdOptions struct {
	Endpoints    []string
	Prefix       string
	DialTimeout  time.Duration
	DrainTimeout time.Duration
}

// Applied is one config or canary document after the manager has accepted it.
type Applied struct {
	Name      string
	Kind      string
	Version   int
	Percent   int
	Draining  int
	DrainLeft int
}

// EtcdSource implements Source by watching an etcd prefix.
// A nil source means endpoints were not configured and the memory manager is used.
type EtcdSource struct {
	client   *clientv3.Client
	prefix   string
	drain    time.Duration
	updates  chan Update
	revision int64
	OnApply  func(Applied)
	OnError  func(error)
}

// OptionsFromEnv reads AGENTLOOM_ETCD_ENDPOINTS and AGENTLOOM_ETCD_PREFIX.
// The boolean is false when the endpoint variable is unset or empty.
func OptionsFromEnv(getenv func(string) string) (EtcdOptions, bool, error) {
	if getenv == nil {
		return EtcdOptions{}, false, nil
	}
	raw := strings.TrimSpace(getenv("AGENTLOOM_ETCD_ENDPOINTS"))
	if raw == "" {
		return EtcdOptions{}, false, nil
	}
	var endpoints []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			endpoints = append(endpoints, part)
		}
	}
	if len(endpoints) == 0 {
		return EtcdOptions{}, false, fmt.Errorf("config: AGENTLOOM_ETCD_ENDPOINTS is set but empty")
	}
	return EtcdOptions{
		Endpoints:    endpoints,
		Prefix:       strings.TrimSpace(getenv("AGENTLOOM_ETCD_PREFIX")),
		DialTimeout:  5 * time.Second,
		DrainTimeout: 5 * time.Second,
	}, true, nil
}

// DialEtcd connects to etcd and checks the first endpoint before returning.
func DialEtcd(ctx context.Context, opts EtcdOptions) (*EtcdSource, error) {
	if len(opts.Endpoints) == 0 {
		return nil, fmt.Errorf("config: etcd endpoints empty")
	}
	timeout := opts.DialTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   opts.Endpoints,
		DialTimeout: timeout,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		return nil, err
	}
	ping, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := client.Status(ping, opts.Endpoints[0]); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("config: etcd status: %w", err)
	}
	prefix := opts.Prefix
	if prefix == "" {
		prefix = defaultEtcdPrefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	drain := opts.DrainTimeout
	if drain <= 0 {
		drain = 5 * time.Second
	}
	return &EtcdSource{client: client, prefix: prefix, drain: drain, updates: make(chan Update, 16)}, nil
}

// Updates returns stable config documents observed by Sync and Watch.
// The channel drops documents when the buffer is full.
func (s *EtcdSource) Updates() <-chan Update {
	if s == nil || s.updates == nil {
		ch := make(chan Update)
		close(ch)
		return ch
	}
	return s.updates
}

// Close releases the etcd client.
func (s *EtcdSource) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Sync loads the current prefix and applies it before Watch starts.
func (s *EtcdSource) Sync(ctx context.Context, m *Manager) error {
	resp, err := s.client.Get(ctx, s.prefix, clientv3.WithPrefix())
	if err != nil {
		return err
	}
	s.revision = resp.Header.Revision
	sort.Slice(resp.Kvs, func(i, j int) bool {
		return etcdKeyRank(string(resp.Kvs[i].Key)) < etcdKeyRank(string(resp.Kvs[j].Key))
	})
	for _, kv := range resp.Kvs {
		applied, err := ApplyEtcdKV(m, s.prefix, string(kv.Key), kv.Value, false, s.drain)
		if err != nil {
			return err
		}
		s.note(applied, kv.Value, false)
	}
	return nil
}

// Watch applies later changes. Call Sync first so the revision cursor is set.
func (s *EtcdSource) Watch(ctx context.Context, m *Manager) error {
	wch := s.client.Watch(ctx, s.prefix, clientv3.WithPrefix(), clientv3.WithRev(s.revision+1))
	for resp := range wch {
		if err := resp.Err(); err != nil {
			return err
		}
		for _, ev := range resp.Events {
			deleted := ev.Type == clientv3.EventTypeDelete
			var value []byte
			key := ""
			if ev.Kv != nil {
				key = string(ev.Kv.Key)
				if !deleted {
					value = ev.Kv.Value
				}
			}
			applied, err := ApplyEtcdKV(m, s.prefix, key, value, deleted, s.drain)
			if err != nil {
				if s.OnError != nil {
					s.OnError(err)
				}
				continue
			}
			s.note(applied, value, deleted)
		}
	}
	return nil
}

func (s *EtcdSource) note(applied Applied, value []byte, deleted bool) {
	if s.OnApply != nil {
		s.OnApply(applied)
	}
	if applied.Kind == "config" && !deleted && s.updates != nil {
		doc, err := decodeEtcdDocument(value)
		if err != nil {
			return
		}
		select {
		case s.updates <- Update{Name: applied.Name, Cfg: doc.config(applied.Name)}:
		default:
		}
	}
}

type etcdDocument struct {
	Rollback      bool     `json:"rollback"`
	Percent       int      `json:"percent"`
	SystemPrompt  string   `json:"system_prompt"`
	Model         string   `json:"model"`
	MaxIter       int      `json:"max_iter"`
	Tools         []string `json:"tools,omitempty"`
	ChunkStrategy string   `json:"chunk_strategy"`
	PromptMode    string   `json:"prompt_mode,omitempty"`
	TopK          int      `json:"top_k,omitempty"`
}

func (d etcdDocument) config(name string) Config {
	return Config{
		Name:          name,
		SystemPrompt:  d.SystemPrompt,
		Model:         d.Model,
		MaxIter:       d.MaxIter,
		Tools:         append([]string(nil), d.Tools...),
		ChunkStrategy: d.ChunkStrategy,
		PromptMode:    d.PromptMode,
		TopK:          d.TopK,
	}
}

// ApplyEtcdKV decodes one key and updates the manager.
// Stable keys use Reload and then Drain. Canary keys use StartCanary,
// SetCanaryPercent, PromoteCanary, and RollbackCanary.
func ApplyEtcdKV(m *Manager, prefix, key string, value []byte, deleted bool, drainFor time.Duration) (Applied, error) {
	name, kind, ok := parseEtcdKey(prefix, key)
	if !ok {
		return Applied{}, nil
	}
	if deleted {
		if kind != "canary" {
			return Applied{Name: name, Kind: kind}, nil
		}
		return rollbackCanaryKey(m, name, drainFor)
	}
	doc, err := decodeEtcdDocument(value)
	if err != nil {
		return Applied{}, err
	}
	switch kind {
	case "config":
		return applyStableDocument(m, name, doc, drainFor)
	case "canary":
		return applyCanaryDocument(m, name, doc, drainFor)
	default:
		return Applied{}, nil
	}
}

func applyStableDocument(m *Manager, name string, doc etcdDocument, drainFor time.Duration) (Applied, error) {
	cfg := doc.config(name)
	if cfg.SystemPrompt == "" {
		return Applied{}, fmt.Errorf("config: etcd document requires a system prompt")
	}
	draining := m.Reload(name, cfg)
	left := 0
	if draining > 0 {
		left = m.Drain(name, drainFor)
	}
	handle, err := m.Acquire(name)
	if err != nil {
		return Applied{}, err
	}
	version := handle.Config().Version
	handle.Release()
	return Applied{Name: name, Kind: "config", Version: version, Draining: draining, DrainLeft: left}, nil
}

func applyCanaryDocument(m *Manager, name string, doc etcdDocument, drainFor time.Duration) (Applied, error) {
	if doc.Rollback || doc.Percent == 0 {
		return rollbackCanaryKey(m, name, drainFor)
	}
	cfg := doc.config(name)
	current, percent, active := m.CanaryConfig(name)
	if !active {
		version, err := m.StartCanary(name, cfg, doc.Percent)
		if err != nil {
			return Applied{}, err
		}
		if doc.Percent == 100 {
			if err := m.PromoteCanary(name); err != nil {
				return Applied{}, err
			}
			_ = m.Drain(name, drainFor)
			return Applied{Name: name, Kind: "promote", Version: version, Percent: 100}, nil
		}
		return Applied{Name: name, Kind: "canary", Version: version, Percent: doc.Percent}, nil
	}
	if !sameCandidate(current, cfg) {
		if _, err := rollbackCanaryKey(m, name, drainFor); err != nil {
			return Applied{}, err
		}
		version, err := m.StartCanary(name, cfg, doc.Percent)
		if err != nil {
			return Applied{}, err
		}
		if doc.Percent == 100 {
			if err := m.PromoteCanary(name); err != nil {
				return Applied{}, err
			}
			_ = m.Drain(name, drainFor)
			return Applied{Name: name, Kind: "promote", Version: version, Percent: 100}, nil
		}
		return Applied{Name: name, Kind: "canary", Version: version, Percent: doc.Percent}, nil
	}
	if percent != doc.Percent {
		if err := m.SetCanaryPercent(name, doc.Percent); err != nil {
			return Applied{}, err
		}
	}
	if doc.Percent == 100 {
		if err := m.PromoteCanary(name); err != nil {
			return Applied{}, err
		}
		_ = m.Drain(name, drainFor)
		return Applied{Name: name, Kind: "promote", Version: current.Version, Percent: 100}, nil
	}
	return Applied{Name: name, Kind: "canary", Version: current.Version, Percent: doc.Percent}, nil
}

func rollbackCanaryKey(m *Manager, name string, drainFor time.Duration) (Applied, error) {
	if _, ok := m.CanaryPercent(name); !ok {
		return Applied{Name: name, Kind: "rollback"}, nil
	}
	if err := m.RollbackCanary(name); err != nil {
		return Applied{}, err
	}
	left := m.Drain(name, drainFor)
	return Applied{Name: name, Kind: "rollback", DrainLeft: left}, nil
}

func decodeEtcdDocument(value []byte) (etcdDocument, error) {
	if len(strings.TrimSpace(string(value))) == 0 {
		return etcdDocument{}, fmt.Errorf("config: empty etcd document")
	}
	var doc etcdDocument
	if err := json.Unmarshal(value, &doc); err != nil {
		return etcdDocument{}, fmt.Errorf("config: etcd document: %w", err)
	}
	return doc, nil
}

func parseEtcdKey(prefix, key string) (name, kind string, ok bool) {
	if prefix == "" {
		prefix = defaultEtcdPrefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(key, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, prefix)
	name, kind, found := strings.Cut(rest, "/")
	if !found || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	if kind != "config" && kind != "canary" {
		return "", "", false
	}
	return name, kind, true
}

func sameCandidate(a, b Config) bool {
	if a.SystemPrompt != b.SystemPrompt || a.Model != b.Model || a.MaxIter != b.MaxIter || a.ChunkStrategy != b.ChunkStrategy || a.PromptMode != b.PromptMode || a.TopK != b.TopK {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for i := range a.Tools {
		if a.Tools[i] != b.Tools[i] {
			return false
		}
	}
	return true
}

func etcdKeyRank(key string) int {
	if strings.HasSuffix(key, "/config") {
		return 0
	}
	return 1
}
