package ranking

import (
	"sort"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/store"
)

// ComputeRankScore calculates a deterministic node rank in [0.1, 10.0] from throughput, capacity, and freshness.
func ComputeRankScore(n *store.NodeRecord, now time.Time) float64 {
	if n.RevokedAt != nil || n.Status != "online" {
		return 0
	}
	score := 1.0
	if n.ThroughputTPS != nil && *n.ThroughputTPS > 0 {
		tps := *n.ThroughputTPS
		if tps > 100 {
			tps = 100
		}
		score += tps / 20.0
	}
	freeSlots := n.MaxSlots - n.ActiveSlots
	if freeSlots > 0 {
		score += float64(freeSlots) * 0.25
	}
	if n.LastSeenAt != nil && now.Sub(*n.LastSeenAt) > 30*time.Second {
		score *= 0.5
	}
	if score > 10.0 {
		score = 10.0
	}
	return score
}

// SortNodeIDs orders node IDs by descending RankScore.
func SortNodeIDs(ids []string, nodes map[string]*store.NodeRecord) {
	sort.SliceStable(ids, func(i, j int) bool {
		ni := nodes[ids[i]]
		nj := nodes[ids[j]]
		var si, sj float64
		if ni != nil {
			si = ni.RankScore
		}
		if nj != nil {
			sj = nj.RankScore
		}
		return si > sj
	})
}
