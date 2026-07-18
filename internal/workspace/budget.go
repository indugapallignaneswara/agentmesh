package workspace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// ErrBudgetExceeded is returned when an agent's room or per-member daily byte
// budget is exhausted. It is retryable — the budget resets at UTC midnight.
var ErrBudgetExceeded = errors.New("budget exceeded (daily coordination-byte budget; resets at UTC midnight)")

// budgetWarnFraction is the soft-warning threshold: when a room's spend
// crosses this fraction of its budget, one budget_warning event is appended to
// the room's log (once per room per day).
const budgetWarnFraction = 0.8

// budgetTracker is the in-memory spend state the metering middleware consults
// on every tool call. It is deliberately synchronous and coarse (a mutex over
// maps — nanoseconds) so enforcement never waits on the store. Persistence
// comes from the usage ledger: on the first call for a (room, day) the tracker
// seeds itself from UsageSummary, which is how a budget survives kill -9 — the
// ledger is the memory, the tracker is the cache.
type budgetTracker struct {
	mu    sync.Mutex
	day   string // UTC day currently tracked; rollover resets all state
	rooms map[string]*roomBudget
}

type roomBudget struct {
	seeded      bool
	spentTotal  int64
	spentMember map[string]int64
	warned      bool

	// cached room policy + member kinds (refreshed on TTL so a budget change
	// takes effect within budgetPolicyTTL, not at restart).
	budget       int64
	memberBudget int64
	policyAt     time.Time
	kinds        map[string]model.Kind
	kindsAt      map[string]time.Time
}

const (
	budgetPolicyTTL = 15 * time.Second
	budgetKindTTL   = 60 * time.Second
)

func newBudgetTracker() *budgetTracker {
	return &budgetTracker{rooms: map[string]*roomBudget{}}
}

// room returns the state for a workspace, handling UTC-day rollover.
func (t *budgetTracker) room(ws string, now time.Time) *roomBudget {
	day := now.UTC().Format("2006-01-02")
	if t.day != day {
		t.day = day
		t.rooms = map[string]*roomBudget{}
	}
	rb, ok := t.rooms[ws]
	if !ok {
		rb = &roomBudget{spentMember: map[string]int64{}, kinds: map[string]model.Kind{}, kindsAt: map[string]time.Time{}}
		t.rooms[ws] = rb
	}
	return rb
}

// BudgetCheck is the pre-call gate the metering middleware runs before a tool
// executes. Humans are never blocked — a runaway agent must not be able to
// silence the moderators who would stop it (the rate-limit philosophy).
// Unattributable calls (no workspace or member) pass: blocking what cannot be
// fairly attributed would punish the wrong party.
func (s *Service) BudgetCheck(ctx context.Context, ws, member string) error {
	if ws == "" || member == "" {
		return nil
	}
	now := s.now()

	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	rb := s.budget.room(ws, now)

	s.refreshBudgetPolicyLocked(ctx, ws, rb, now)

	// Principal budget claim (Agent-IAM): the credential itself may carry a
	// per-principal daily cap.
	var claimCap int64
	p, hasPrincipal := auth.FromContext(ctx)
	if hasPrincipal && p.Member == member {
		claimCap = p.BudgetDailyBytes
	}

	if rb.budget == 0 && rb.memberBudget == 0 && claimCap == 0 {
		return nil // nothing to enforce
	}

	kind := s.memberKindLocked(ctx, ws, member, rb, now, hasPrincipal, p)
	if kind == model.KindHuman {
		return nil
	}

	if !rb.seeded {
		s.seedBudgetLocked(ctx, ws, rb, now)
	}

	if rb.budget > 0 && rb.spentTotal >= rb.budget {
		return fmt.Errorf("%w: room %q spent %d of %d bytes today", ErrBudgetExceeded, ws, rb.spentTotal, rb.budget)
	}
	cap := rb.memberBudget
	if claimCap > 0 && (cap == 0 || claimCap < cap) {
		cap = claimCap
	}
	if cap > 0 && rb.spentMember[member] >= cap {
		return fmt.Errorf("%w: member %q spent %d of %d bytes today", ErrBudgetExceeded, member, rb.spentMember[member], cap)
	}
	return nil
}

// BudgetSpend records metered bytes against the caller after a tool call.
// Human bytes are not charged (humans are exempt from blocking, and charging
// them would let a chatty human exhaust the agents' shared room budget).
func (s *Service) BudgetSpend(ctx context.Context, ws, member string, bytes int64) {
	if ws == "" || member == "" || bytes <= 0 {
		return
	}
	now := s.now()

	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	rb := s.budget.room(ws, now)
	s.refreshBudgetPolicyLocked(ctx, ws, rb, now)
	if rb.budget == 0 && rb.memberBudget == 0 {
		// Still track per-member spend when only credential caps exist; cheap
		// and keeps claim enforcement correct.
		p, ok := auth.FromContext(ctx)
		if !ok || p.BudgetDailyBytes == 0 {
			return
		}
	}

	p, hasPrincipal := auth.FromContext(ctx)
	kind := s.memberKindLocked(ctx, ws, member, rb, now, hasPrincipal, p)
	if kind == model.KindHuman {
		return
	}
	if !rb.seeded {
		s.seedBudgetLocked(ctx, ws, rb, now)
	}

	rb.spentTotal += bytes
	rb.spentMember[member] += bytes

	// Soft warning at 80%: once per room per day, into the room's own event
	// log so every member (and the dashboard) sees it coming.
	if rb.budget > 0 && !rb.warned && float64(rb.spentTotal) >= budgetWarnFraction*float64(rb.budget) {
		rb.warned = true
		s.appendEvent(ctx, ws, "system", "budget_warning", map[string]any{
			"spent_bytes":  rb.spentTotal,
			"budget_bytes": rb.budget,
			"note":         "80% of the room's daily agent byte budget consumed",
		})
	}
}

// BudgetInvalidate drops the cached budget policy for a room so a just-set
// budget takes effect immediately rather than after the policy TTL.
// RoomSetBudget calls this; it is also safe to call from tests.
func (s *Service) BudgetInvalidate(ws string) {
	s.budget.mu.Lock()
	defer s.budget.mu.Unlock()
	if rb, ok := s.budget.rooms[ws]; ok {
		rb.policyAt = time.Time{}
	}
}

// refreshBudgetPolicyLocked caches the room's budget columns with a short TTL
// so `room_set_budget` takes effect within seconds without a hot-path read.
func (s *Service) refreshBudgetPolicyLocked(ctx context.Context, ws string, rb *roomBudget, now time.Time) {
	if now.Sub(rb.policyAt) < budgetPolicyTTL && !rb.policyAt.IsZero() {
		return
	}
	rb.policyAt = now
	w, err := s.store.GetWorkspace(ctx, ws)
	if err != nil {
		// Unknown room or store hiccup: keep previous values (zero on first
		// sight, i.e. unenforced) — enforcement must not invent a budget.
		return
	}
	rb.budget = w.BudgetDailyBytes
	rb.memberBudget = w.BudgetMemberDailyBytes
}

// memberKindLocked resolves a member's kind for exemption decisions: the
// verified Principal when present, else the membership record (cached).
// Unknown members default to agent — the safe direction, since humans are the
// exemption and a human blocked by a stale cache can still be identified by
// their Principal or membership row on the next refresh.
func (s *Service) memberKindLocked(ctx context.Context, ws, member string, rb *roomBudget, now time.Time, hasPrincipal bool, p auth.Principal) model.Kind {
	if hasPrincipal && p.Member == member && p.Kind.Valid() {
		return p.Kind
	}
	if at, ok := rb.kindsAt[member]; ok && now.Sub(at) < budgetKindTTL {
		return rb.kinds[member]
	}
	kind := model.KindAgent
	if m, err := s.store.GetMember(ctx, ws, member); err == nil && m.Kind.Valid() {
		kind = m.Kind
	}
	rb.kinds[member] = kind
	rb.kindsAt[member] = now
	return kind
}

// seedBudgetLocked loads today's agent spend from the usage ledger — the
// mechanism by which budgets survive a process restart (including kill -9):
// the flushed ledger is authoritative, the tracker is a warm cache over it.
// Bytes lost to an unflushed recorder buffer are the documented tolerance.
func (s *Service) seedBudgetLocked(ctx context.Context, ws string, rb *roomBudget, now time.Time) {
	rb.seeded = true
	dayStart := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	sums, err := s.store.UsageSummary(ctx, ws, dayStart, now.UTC().Add(time.Minute))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("budget seed failed; enforcing from zero", "workspace", ws, "err", err)
		}
		return
	}
	for _, m := range sums {
		if m.Kind == model.KindHuman {
			continue
		}
		spent := m.IngressBytes + m.EgressBytes
		rb.spentTotal += spent
		if m.Member != "" {
			rb.spentMember[m.Member] += spent
		}
	}
}
