package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// usageDayAgg is a batch's pre-aggregated contribution to one usage_daily row.
type usageDayAgg struct {
	workspace, member         string
	day                       time.Time
	ingressBytes, egressBytes int64
	events                    int64
	reportedPrompt            int64
	reportedCompletion        int64
}

// textArg stores NULL for an empty string (reported-row metadata columns).
func textArg(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Postgres) AppendUsage(ctx context.Context, events []model.UsageEvent) error {
	if len(events) == 0 {
		return nil
	}

	// Pre-aggregate the batch per (workspace, member, UTC day) so the daily
	// rollup is one upsert per touched bucket instead of one per event.
	aggs := make(map[string]*usageDayAgg)
	var keys []string // deterministic upsert order (stable across retries)
	for _, e := range events {
		day := utcDay(e.TS)
		k := usageDayKey(e.Workspace, e.Member, day)
		a, ok := aggs[k]
		if !ok {
			a = &usageDayAgg{workspace: e.Workspace, member: e.Member, day: day}
			aggs[k] = a
			keys = append(keys, k)
		}
		a.events++
		switch e.Direction {
		case model.UsageIngress:
			a.ingressBytes += e.Bytes
		case model.UsageEgress:
			a.egressBytes += e.Bytes
		case model.UsageReported:
			a.reportedPrompt += e.ReportedPromptTokens
			a.reportedCompletion += e.ReportedCompletionTokens
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after a successful commit

	batch := &pgx.Batch{}
	for _, e := range events {
		var rp, rc any
		if e.Direction == model.UsageReported {
			rp, rc = e.ReportedPromptTokens, e.ReportedCompletionTokens
		}
		batch.Queue(`
			INSERT INTO usage_events
				(ts, workspace, member, kind, tool, direction, bytes, authenticated,
				 reported_prompt_tokens, reported_completion_tokens, vendor, model)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			e.TS, e.Workspace, e.Member, string(e.Kind), e.Tool, string(e.Direction),
			e.Bytes, e.Authenticated, rp, rc, textArg(e.Vendor), textArg(e.Model))
	}
	for _, k := range keys {
		a := aggs[k]
		batch.Queue(`
			INSERT INTO usage_daily
				(workspace, member, day, ingress_bytes, egress_bytes, events,
				 reported_prompt_tokens, reported_completion_tokens)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace, member, day) DO UPDATE SET
				ingress_bytes              = usage_daily.ingress_bytes + EXCLUDED.ingress_bytes,
				egress_bytes               = usage_daily.egress_bytes + EXCLUDED.egress_bytes,
				events                     = usage_daily.events + EXCLUDED.events,
				reported_prompt_tokens     = usage_daily.reported_prompt_tokens + EXCLUDED.reported_prompt_tokens,
				reported_completion_tokens = usage_daily.reported_completion_tokens + EXCLUDED.reported_completion_tokens`,
			a.workspace, a.member, a.day, a.ingressBytes, a.egressBytes,
			a.events, a.reportedPrompt, a.reportedCompletion)
	}

	br := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Postgres) UsageSummary(ctx context.Context, workspace string, from, to time.Time) ([]model.UsageSummary, error) {
	// Kind: the most recently seen kind per member wins (id breaks ts ties the
	// same way the in-memory store's append order does).
	const q = `
		SELECT member,
		       COALESCE((array_agg(kind ORDER BY ts DESC, id DESC) FILTER (WHERE kind <> ''))[1], '') AS kind,
		       COALESCE(SUM(bytes) FILTER (WHERE direction = 'ingress'), 0),
		       COALESCE(SUM(bytes) FILTER (WHERE direction = 'egress'), 0),
		       COUNT(*),
		       COALESCE(SUM(reported_prompt_tokens), 0),
		       COALESCE(SUM(reported_completion_tokens), 0)
		FROM usage_events
		WHERE workspace = $1 AND ts >= $2 AND ts < $3
		GROUP BY member
		ORDER BY member`
	rows, err := s.pool.Query(ctx, q, workspace, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageSummary
	for rows.Next() {
		sum := model.UsageSummary{Workspace: workspace}
		var kind string
		if err := rows.Scan(&sum.Member, &kind, &sum.IngressBytes, &sum.EgressBytes,
			&sum.Events, &sum.ReportedPromptTokens, &sum.ReportedCompletionTokens); err != nil {
			return nil, err
		}
		sum.Kind = model.Kind(kind)
		out = append(out, sum)
	}
	return out, rows.Err()
}

func (s *Postgres) UsageDaily(ctx context.Context, workspace string, days int) ([]model.UsageDay, error) {
	if days <= 0 {
		days = defaultUsageDays
	}
	// Cutoff computed in Go from time.Now().UTC() (not current_date) so both
	// stores share one clock and the window is testable.
	cutoff := utcDay(time.Now()).AddDate(0, 0, -(days - 1))

	const q = `
		SELECT workspace, member, day, ingress_bytes, egress_bytes, events
		FROM usage_daily
		WHERE workspace = $1 AND day >= $2
		ORDER BY day DESC, member ASC`
	rows, err := s.pool.Query(ctx, q, workspace, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UsageDay
	for rows.Next() {
		var d model.UsageDay
		if err := rows.Scan(&d.Workspace, &d.Member, &d.Day,
			&d.IngressBytes, &d.EgressBytes, &d.Events); err != nil {
			return nil, err
		}
		// Normalise the scanned DATE to the model's midnight-UTC contract by
		// its calendar components (no zone conversion, which could shift days).
		d.Day = time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, time.UTC)
		out = append(out, d)
	}
	return out, rows.Err()
}
