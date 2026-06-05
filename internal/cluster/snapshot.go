package cluster

import "sort"

// NodeView is one node's state for the dashboard.
type NodeView struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	BaseURL  string      `json:"base_url"`
	Source   string      `json:"source"`
	Online   bool        `json:"online"`
	LastErr  string      `json:"last_err,omitempty"`
	SpeedBPS int64       `json:"speed_bps"`
	Jobs     []JobReport `json:"jobs"`
}

// GameView is one title's overall progress.
type GameView struct {
	Title      string `json:"title"`
	TotalParts int    `json:"total_parts"`
	HaveParts  int    `json:"have_parts"`
	TotalBytes int64  `json:"total_bytes"`
	DoneBytes  int64  `json:"done_bytes"`
}

// Snapshot is the whole cluster state the dashboard renders.
type Snapshot struct {
	Nodes               []NodeView `json:"nodes"`
	Games               []GameView `json:"games"`
	AccumulatedSpeedBPS int64      `json:"accumulated_speed_bps"`
	PendingParts        int        `json:"pending_parts"`
	AssignedParts       int        `json:"assigned_parts"`
	DoneParts           int        `json:"done_parts"`
}

// Snapshot returns the current cluster state. Per-node speed is the sum of that
// node's active jobs; the accumulated speed is the sum across all nodes —
// exactly the "combined download speed" the dashboard shows.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	var snap Snapshot
	for _, n := range m.nodes {
		nv := NodeView{ID: n.id, Name: n.name, BaseURL: n.baseURL, Source: n.source, Online: n.online, LastErr: n.lastErr}
		for _, jr := range n.reports {
			nv.Jobs = append(nv.Jobs, jr)
			if jr.State == "active" {
				nv.SpeedBPS += jr.SpeedBPS
			}
		}
		sort.Slice(nv.Jobs, func(i, j int) bool { return nv.Jobs[i].Basename < nv.Jobs[j].Basename })
		snap.AccumulatedSpeedBPS += nv.SpeedBPS
		snap.Nodes = append(snap.Nodes, nv)
	}
	sort.Slice(snap.Nodes, func(i, j int) bool { return snap.Nodes[i].ID < snap.Nodes[j].ID })

	for _, it := range m.work {
		switch it.state {
		case workPending:
			snap.PendingParts++
		case workAssigned:
			snap.AssignedParts++
		case workDone:
			snap.DoneParts++
		}
	}

	titles := make([]string, 0, len(m.games))
	for t := range m.games {
		titles = append(titles, t)
	}
	sort.Strings(titles)
	for _, t := range titles {
		g := m.games[t]
		gv := GameView{Title: t, TotalParts: len(g.parts)}
		for _, p := range g.parts {
			gv.TotalBytes += p.Size
			if m.lib != nil && m.lib.HasBasename(p.Basename) {
				gv.HaveParts++
				gv.DoneBytes += p.Size
			}
		}
		snap.Games = append(snap.Games, gv)
	}
	return snap
}

// Nodes returns a lightweight list of registered nodes (for the nodes API).
func (m *Manager) Nodes() []NodeView {
	return m.Snapshot().Nodes
}
