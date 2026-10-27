package config

// Source produces config changes for the Manager to reload. EtcdSource watches
// an etcd prefix when endpoints are configured. ChannelSource is the in-process
// source used by tests and by agentd when AGENTLOOM_ETCD_ENDPOINTS is empty.
// Stable documents go through Reload and Drain. Canary documents call the
// existing percent split, widen, promote, and rollback methods.

// Source emits config updates for one or more agent names.
type Source interface {
	// Updates returns a channel of (name, Config) pairs; close on context done.
	Updates() <-chan Update
}

// Update pairs an agent name with its new config.
type Update struct {
	Name string
	Cfg  Config
}

// ChannelSource is an in-process Source for tests and the local demo.
type ChannelSource struct {
	ch chan Update
}

// NewChannelSource returns a ChannelSource with the given buffer size.
func NewChannelSource(buf int) *ChannelSource {
	return &ChannelSource{ch: make(chan Update, buf)}
}

// Updates returns the update channel.
func (s *ChannelSource) Updates() <-chan Update { return s.ch }

// Emit pushes an update. Non-blocking: drops if the buffer is full (callers in
// tests control pacing).
func (s *ChannelSource) Emit(name string, cfg Config) {
	select {
	case s.ch <- Update{Name: name, Cfg: cfg}:
	default:
	}
}

// Apply wires a Source to a Manager: every Update triggers a Reload, and the
// returned channel receives the in-flight refcount of each superseded entry
// (so a watcher can log drain pressure or back off when it is non-zero).
func Apply(m *Manager, src Source) <-chan int {
	out := make(chan int, 16)
	go func() {
		defer close(out)
		for u := range src.Updates() {
			out <- m.Reload(u.Name, u.Cfg)
		}
	}()
	return out
}
