package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
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

// maxUsageLabel caps the vendor/model strings on a reported-usage row: they
// are display labels, not payloads.
const maxUsageLabel = 64

// Aliases so ReportUsage can keep its documented `model` parameter name (the
// vendor model identifier) without shadowing the model package in its body.
type reportedUsageEvent = model.UsageEvent

const usageReportedDirection = model.UsageReported

// ReportUsage appends one client-REPORTED vendor-usage row to the ledger:
// the prompt/completion tokens the member claims its own LLM calls consumed.
// These numbers are unverified claims (docs/token-metering.md §5) — they are
// stored under direction=reported, displayed as "reported", and never merged
// into platform-measured byte series. With auth on, CheckActor ensures a
// member can only report as itself; magnitude is taken on faith and labelled.
// The append is synchronous — an explicit report is not the hot path.
func (s *Service) ReportUsage(ctx context.Context, workspace, member string, promptTokens, completionTokens int64, vendor, model string) error {
	if err := validName("workspace", workspace); err != nil {
		return err
	}
	if err := validName("member", member); err != nil {
		return err
	}
	if err := auth.CheckActor(ctx, workspace, member); err != nil {
		return err
	}
	if promptTokens < 0 || completionTokens < 0 {
		return fmt.Errorf("%w: token counts must be >= 0", ErrInvalidInput)
	}
	if promptTokens == 0 && completionTokens == 0 {
		return fmt.Errorf("%w: at least one of prompt_tokens/completion_tokens must be > 0", ErrInvalidInput)
	}
	if len(vendor) > maxUsageLabel || len(model) > maxUsageLabel {
		return fmt.Errorf("%w: vendor and model must be at most %d bytes", ErrInvalidInput, maxUsageLabel)
	}

	mem, err := s.store.GetMember(ctx, workspace, member)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("member %q: %w", member, store.ErrNotFound)
		}
		return err
	}
	_, authenticated := auth.FromContext(ctx)

	ev := reportedUsageEvent{
		TS:                       s.now().UTC(),
		Workspace:                workspace,
		Member:                   member,
		Kind:                     mem.Kind,
		Tool:                     "usage_report",
		Direction:                usageReportedDirection,
		Authenticated:            authenticated,
		ReportedPromptTokens:     promptTokens,
		ReportedCompletionTokens: completionTokens,
		Vendor:                   vendor,
		Model:                    model,
	}
	return s.store.AppendUsage(ctx, []reportedUsageEvent{ev})
}
