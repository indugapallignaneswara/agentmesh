package store

import (
	"context"
	"sort"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// defaultUsageDays is the UsageDaily window when the caller passes days <= 0.
const defaultUsageDays = 30

// usageDayKey builds the daily-rollup map key. The day component is the UTC
// calendar date, mirroring the (workspace, member, day) primary key of the
// Postgres usage_daily table.
func usageDayKey(workspace, member string, day time.Time) string {
	return workspace + "\x00" + member + "\x00" + day.Format("2006-01-02")
}

// utcDay truncates ts to its UTC calendar day (midnight UTC).
func utcDay(ts time.Time) time.Time {
	u := ts.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func (s *Memory) AppendUsage(_ context.Context, events []model.UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.daily == nil {
		s.daily = make(map[string]*model.UsageDay)
	}
	for _, e := range events {
		s.usage = append(s.usage, e)

		day := utcDay(e.TS)
		k := usageDayKey(e.Workspace, e.Member, day)
		d, ok := s.daily[k]
		if !ok {
			d = &model.UsageDay{Workspace: e.Workspace, Member: e.Member, Day: day}
			s.daily[k] = d
		}
		d.Events++
		switch e.Direction {
		case model.UsageIngress:
			d.IngressBytes += e.Bytes
		case model.UsageEgress:
			d.EgressBytes += e.Bytes
		}
		// Reported rows carry vendor token claims, not platform-measured
		// bytes; they count as events but never contribute to byte columns.
	}
	return nil
}

func (s *Memory) UsageSummary(_ context.Context, workspace string, from, to time.Time) ([]model.UsageSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sums := make(map[string]*model.UsageSummary)
	lastTS := make(map[string]time.Time) // last-seen kind per member wins
	for _, e := range s.usage {
		if e.Workspace != workspace || e.TS.Before(from) || !e.TS.Before(to) {
			continue
		}
		sum, ok := sums[e.Member]
		if !ok {
			sum = &model.UsageSummary{Workspace: workspace, Member: e.Member}
			sums[e.Member] = sum
		}
		sum.Events++
		switch e.Direction {
		case model.UsageIngress:
			sum.IngressBytes += e.Bytes
		case model.UsageEgress:
			sum.EgressBytes += e.Bytes
		case model.UsageReported:
			sum.ReportedPromptTokens += e.ReportedPromptTokens
			sum.ReportedCompletionTokens += e.ReportedCompletionTokens
		}
		// Last-seen NON-EMPTY kind wins: most tool calls carry no kind arg in
		// auth-off mode, and an empty kind must not blank out a known one.
		if e.Kind != "" && !e.TS.Before(lastTS[e.Member]) {
			lastTS[e.Member] = e.TS
			sum.Kind = e.Kind
		}
	}

	out := make([]model.UsageSummary, 0, len(sums))
	for _, sum := range sums {
		out = append(out, *sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Member < out[j].Member })
	return out, nil
}

func (s *Memory) UsageDaily(_ context.Context, workspace string, days int) ([]model.UsageDay, error) {
	if days <= 0 {
		days = defaultUsageDays
	}
	// The window is the last `days` UTC calendar days ending today, matching
	// the Postgres cutoff (day >= today - (days-1)).
	cutoff := utcDay(time.Now()).AddDate(0, 0, -(days - 1))

	s.mu.Lock()
	defer s.mu.Unlock()
	var out []model.UsageDay
	for _, d := range s.daily {
		if d.Workspace != workspace || d.Day.Before(cutoff) {
			continue
		}
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Day.Equal(out[j].Day) {
			return out[i].Day.After(out[j].Day) // newest day first
		}
		return out[i].Member < out[j].Member
	})
	return out, nil
}
