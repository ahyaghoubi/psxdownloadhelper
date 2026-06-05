package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Library is the master's read view of its own library: a part is "have it"
// once present here (downloaded by a node, or dropped in manually via SSD).
type Library interface {
	HasBasename(name string) bool
}

// Manager is the master orchestrator: it tracks slave nodes, assigns parts,
// polls progress, and pulls finished parts into the master library.
type Manager struct {
	libDir       string
	token        string
	lib          Library
	maxPerNode   int
	pollInterval time.Duration
	logger       *slog.Logger

	mu       sync.Mutex
	inflight sync.WaitGroup
	nodes    map[string]*nodeState
	work     []*workItem
	games    map[string]*gameState
	nextID   int64
}

type nodeState struct {
	id       string
	name     string
	baseURL  string
	source   string // "manual" | "mdns"
	client   nodeTransport
	online   bool
	lastSeen time.Time
	lastErr  string
	assigned map[string]*workItem // jobID → item
	reports  map[string]JobReport // jobID → latest progress
}

type workState string

const (
	workPending  workState = "pending"
	workAssigned workState = "assigned"
	workDone     workState = "done"
)

type workItem struct {
	jobID    string
	title    string
	part     PartURL
	state    workState
	nodeID   string
	failures int
}

type gameState struct {
	title string
	parts []PartURL
}

// Deps configures a Manager.
type Deps struct {
	LibDir       string
	Token        string
	Library      Library
	MaxPerNode   int
	PollInterval time.Duration
	Logger       *slog.Logger
}

// NewManager builds a master Manager.
func NewManager(d Deps) *Manager {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.MaxPerNode <= 0 {
		d.MaxPerNode = 2
	}
	if d.PollInterval <= 0 {
		d.PollInterval = 2 * time.Second
	}
	return &Manager{
		libDir:       d.LibDir,
		token:        d.Token,
		lib:          d.Library,
		maxPerNode:   d.MaxPerNode,
		pollInterval: d.PollInterval,
		logger:       d.Logger,
		nodes:        make(map[string]*nodeState),
		games:        make(map[string]*gameState),
	}
}

// AddNode registers a slave by base URL (manual or mDNS-discovered). It enlists
// the node by calling /node/info; an unreachable node is still recorded but
// marked offline.
func (m *Manager) AddNode(ctx context.Context, baseURL, source string) (string, error) {
	client := newNodeClient(baseURL, m.token)
	info, err := client.Info(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	// De-dupe by base URL.
	for _, n := range m.nodes {
		if n.baseURL == baseURL {
			n.online = err == nil
			if err == nil {
				n.name, n.lastSeen = info.Name, time.Now()
			}
			return n.id, err
		}
	}
	m.nextID++
	id := fmt.Sprintf("node-%d", m.nextID)
	ns := &nodeState{
		id: id, baseURL: baseURL, source: source, client: client,
		assigned: make(map[string]*workItem), reports: make(map[string]JobReport),
	}
	if err == nil {
		ns.online, ns.name, ns.lastSeen = true, info.Name, time.Now()
	} else {
		ns.lastErr = err.Error()
	}
	m.nodes[id] = ns
	m.logger.Info("cluster: node added", "id", id, "url", baseURL, "source", source, "online", ns.online)
	return id, err
}

// AddLocalNode registers the master itself as a download node. It downloads
// straight into the master's library dir (no loopback HTTP, no self-pull).
func (m *Manager) AddLocalNode(ctx context.Context, node nodeTransport, source string) (string, error) {
	info, err := node.Info(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	id := fmt.Sprintf("node-%d", m.nextID)
	ns := &nodeState{
		id: id, name: info.Name, baseURL: "local", source: source, client: node,
		assigned: make(map[string]*workItem), reports: make(map[string]JobReport),
	}
	if err == nil {
		ns.online, ns.name, ns.lastSeen = true, info.Name, time.Now()
	} else {
		ns.lastErr = err.Error()
	}
	m.nodes[id] = ns
	m.logger.Info("cluster: node added", "id", id, "url", ns.baseURL, "source", source, "online", ns.online)
	return id, err
}

// RemoveNode deregisters a node and requeues its in-flight work.
func (m *Manager) RemoveNode(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.nodes[id]
	if n == nil {
		return
	}
	for _, it := range n.assigned {
		it.state, it.nodeID = workPending, ""
	}
	delete(m.nodes, id)
	m.logger.Info("cluster: node removed", "id", id)
}

// Submit registers a game's parts as work. Parts already present in the master
// library (downloaded earlier or dropped in manually) are skipped.
func (m *Manager) Submit(title string, parts []PartURL) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.games[title]; ok {
		return // already tracking this title
	}
	m.games[title] = &gameState{title: title, parts: parts}
	for _, p := range parts {
		if m.lib != nil && m.lib.HasBasename(p.Basename) {
			continue
		}
		m.nextID++
		m.work = append(m.work, &workItem{
			jobID: fmt.Sprintf("job-%d", m.nextID), title: title, part: p, state: workPending,
		})
	}
	m.logger.Info("cluster: game submitted", "title", title, "parts", len(parts), "queued", len(m.work))
}

// Run drives the assign → poll → collect loop until ctx is canceled.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.waitInflight()
			return nil
		case <-ticker.C:
			m.poll(ctx)
			m.assign(ctx)
		}
	}
}

// Tick runs one assign+poll cycle (exposed for deterministic tests).
func (m *Manager) Tick(ctx context.Context) {
	m.poll(ctx)
	m.assign(ctx)
}

// assign hands pending work to online nodes with spare capacity.
func (m *Manager) assign(ctx context.Context) {
	m.mu.Lock()
	type todo struct {
		client nodeTransport
		nodeID string
		item   *workItem
	}
	var todos []todo
	for _, it := range m.work {
		if it.state != workPending {
			continue
		}
		// Skip if already in library (manual drop while queued).
		if m.lib != nil && m.lib.HasBasename(it.part.Basename) {
			it.state = workDone
			continue
		}
		n := m.leastLoadedLocked()
		if n == nil {
			break // no capacity right now
		}
		it.state, it.nodeID = workAssigned, n.id
		n.assigned[it.jobID] = it
		todos = append(todos, todo{client: n.client, nodeID: n.id, item: it})
	}
	m.mu.Unlock()

	for _, t := range todos {
		m.inflight.Add(1)
		err := t.client.Assign(ctx, AssignRequest{JobID: t.item.jobID, URL: t.item.part.URL, Basename: t.item.part.Basename})
		m.inflight.Done()
		if err != nil {
			m.logger.Warn("cluster: assign failed; requeueing", "job", t.item.jobID, "node", t.nodeID, "err", err)
			m.mu.Lock()
			t.item.state, t.item.nodeID = workPending, ""
			if n := m.nodes[t.nodeID]; n != nil {
				delete(n.assigned, t.item.jobID)
			}
			m.mu.Unlock()
		}
	}
}

// leastLoadedLocked returns the online node with the fewest assigned items that
// still has spare capacity, or nil. Caller holds m.mu.
func (m *Manager) leastLoadedLocked() *nodeState {
	var best *nodeState
	for _, n := range m.nodes {
		if !n.online || len(n.assigned) >= m.maxPerNode {
			continue
		}
		if best == nil || len(n.assigned) < len(best.assigned) {
			best = n
		}
	}
	return best
}

// poll refreshes each node's status, collects finished parts, and requeues work
// from nodes that have gone offline.
func (m *Manager) poll(ctx context.Context) {
	m.mu.Lock()
	nodes := make([]*nodeState, 0, len(m.nodes))
	for _, n := range m.nodes {
		nodes = append(nodes, n)
	}
	m.mu.Unlock()

	for _, n := range nodes {
		sr, err := n.client.Status(ctx)
		if err != nil {
			m.markOffline(n, err)
			continue
		}
		m.mu.Lock()
		n.online, n.lastSeen, n.lastErr = true, time.Now(), ""
		reports := make(map[string]JobReport, len(sr.Jobs))
		for _, jr := range sr.Jobs {
			reports[jr.JobID] = jr
		}
		n.reports = reports
		// Find completed jobs to collect.
		var toCollect []*workItem
		for jobID, it := range n.assigned {
			jr, ok := reports[jobID]
			if !ok {
				continue
			}
			switch jr.State {
			case "complete":
				toCollect = append(toCollect, it)
			case "error":
				it.failures++
				it.state, it.nodeID = workPending, ""
				delete(n.assigned, jobID)
				m.logger.Warn("cluster: job errored; requeueing", "job", jobID, "err", jr.Err)
			}
		}
		m.mu.Unlock()

		for _, it := range toCollect {
			m.collect(ctx, n, it)
		}
	}
}

func (m *Manager) waitInflight() {
	done := make(chan struct{})
	go func() {
		m.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		m.logger.Warn("cluster: shutdown timed out waiting for in-flight work")
	}
}

// collect pulls a finished part from the node into the master library.
func (m *Manager) collect(ctx context.Context, n *nodeState, it *workItem) {
	m.inflight.Add(1)
	defer m.inflight.Done()
	if err := n.client.PullPart(ctx, it.part.Basename, m.libDir); err != nil {
		m.logger.Warn("cluster: pull part failed; will retry", "basename", it.part.Basename, "node", n.id, "err", err)
		return
	}
	m.mu.Lock()
	it.state = workDone
	delete(n.assigned, it.jobID)
	m.mu.Unlock()
	_ = n.client.Cancel(ctx, it.jobID) // free the slave's job slot
	m.logger.Info("cluster: part collected", "basename", it.part.Basename, "node", n.id)
}

func (m *Manager) markOffline(n *nodeState, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n.online, n.lastErr = false, err.Error()
	// Requeue this node's in-flight work for other nodes.
	for jobID, it := range n.assigned {
		it.state, it.nodeID = workPending, ""
		delete(n.assigned, jobID)
	}
}
