package workspace

import (
	"context"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/usage"
)

// UsageMemberStats is one member's usage inside the queried window, with the
// display-time token estimate derived from bytes (never stored — see
// docs/token-metering.md §3).
type UsageMemberStats struct {
	model.UsageSummary
	EstTokens int64 `json:"est_tokens"`
}

// UsageStats is a room's usage over a window: per-member rows plus totals.
type UsageStats struct {
	Workspace    string             `json:"workspace"`
	From         time.Time          `json:"from"`
	To           time.Time          `json:"to"`
	Members      []UsageMemberStats `json:"members"`
	IngressBytes int64              `json:"ingress_bytes"`
	EgressBytes  int64              `json:"egress_bytes"`
	EstTokens    int64              `json:"est_tokens"`
	// BytesPerToken is the ratio the estimates were rendered with, so a
	// consumer can re-derive or re-render.
	BytesPerToken float64 `json:"bytes_per_token"`
}

// UsageStatsWindow returns per-member usage for a room over the trailing
// window. Any member of the room may read its burn (usage visibility is a
// coordination feature, not an admin secret); with auth on the token's
// workspace binding is enforced, same as every read.
func (s *Service) UsageStatsWindow(ctx context.Context, workspace string, window time.Duration) (UsageStats, error) {
	if err := validName("workspace", workspace); err != nil {
		return UsageStats{}, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return UsageStats{}, err
	}
	if window <= 0 {
		window = 24 * time.Hour
	}
	to := s.now().UTC()
	from := to.Add(-window)

	sums, err := s.store.UsageSummary(ctx, workspace, from, to)
	if err != nil {
		return UsageStats{}, err
	}
	out := UsageStats{
		Workspace: workspace, From: from, To: to,
		Members:       make([]UsageMemberStats, 0, len(sums)),
		BytesPerToken: s.usageRatio,
	}
	for _, m := range sums {
		est := usage.EstTokens(m.IngressBytes+m.EgressBytes, s.usageRatio)
		out.Members = append(out.Members, UsageMemberStats{UsageSummary: m, EstTokens: est})
		out.IngressBytes += m.IngressBytes
		out.EgressBytes += m.EgressBytes
	}
	out.EstTokens = usage.EstTokens(out.IngressBytes+out.EgressBytes, s.usageRatio)
	return out, nil
}
