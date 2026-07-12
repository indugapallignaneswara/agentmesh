// Package workspace implements the coordination semantics of the event-driven
// blackboard: presence, any-to-any inbox messaging, many-to-many broadcast, and
// an append-only observation log. It is transport-agnostic — the MCP server is
// a thin adapter over this service.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// ErrInvalidInput indicates the caller supplied malformed arguments. It is
// distinct from store.ErrNotFound (a missing principal), so the transport layer
// can map each to the appropriate client-facing error.
var ErrInvalidInput = errors.New("invalid input")

// ErrRoomClosed is returned by write paths when the target room is closed. The
// room stays readable so humans can review it, but new content is rejected.
var ErrRoomClosed = errors.New("room is closed")

// Event type names recorded in the episodic log.
const (
	EventMemberJoined   = "member_joined"
	EventMessageSent    = "message_sent"
	EventBroadcast      = "broadcast"
	EventTaskCreated    = "task_created"
	EventTaskClaimed    = "task_claimed"
	EventTaskCompleted  = "task_completed"
	EventTaskRetried    = "task_retried"
	EventRoomCreated    = "room_created"
	EventRoomClosed     = "room_closed"
	EventRoomReopened   = "room_reopened"
	EventMemberKicked   = "member_kicked"
	EventMemberBanned   = "member_banned"
	EventMemberUnbanned = "member_unbanned"
	EventMemberLeft     = "member_left"
	EventRoleChanged    = "role_changed"

	EventInviteCreated     = "invite_created"
	EventInviteRevoked     = "invite_revoked"
	EventRoomPolicyChanged = "room_policy_changed"
)

// nameRe constrains workspace and member identifiers. They double as NATS
// subject tokens and as identity, so they must be free of dots, spaces and
// wildcards. This also prevents subject-injection via crafted names.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const (
	defaultPresenceTTL   = 60 * time.Second
	defaultEventLimit    = 100
	maxEventLimit        = 1000
	defaultTaskLease     = 5 * time.Minute
	defaultAckVisibility = 60 * time.Second
	maxAckIDs            = 500

	// defaultListLimit / maxListLimit bound every list endpoint so a large
	// workspace cannot return an unbounded payload. Callers that hit the cap
	// are told so explicitly (Truncated) rather than silently losing rows.
	defaultListLimit = 100
	maxListLimit     = 500
	maxTaskTitle     = 512
	maxDependsOn     = 64

	// Input size caps (bytes). These bound the per-call write amplification —
	// a body/payload is fanned out to one delivery row per recipient and
	// replayed on every read — so an agent (agents are loops) cannot exhaust
	// storage or blow other members' context windows with one oversized call.
	maxMessageBody   = 64 * 1024
	maxTaskDetails   = 64 * 1024
	maxTaskResult    = 64 * 1024
	maxEventPayload  = 64 * 1024
	maxAgentCardSize = 64 * 1024
)

// Service is the coordination workspace API.
type Service struct {
	store         store.Store
	bus           bus.Bus
	now           func() time.Time
	newID         func() string
	presenceTTL   time.Duration
	taskLease     time.Duration
	ackVisibility time.Duration // lease window for ack-mode inbox reads
	implicitRoom  bool          // auto-create a room on first join (back-compat / demo mode)
	rl            *limiter      // per-principal rate limits (disabled by default)
	log           *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithClock overrides the time source (used in tests for determinism).
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithIDGen overrides the message ID generator (used in tests).
func WithIDGen(gen func() string) Option { return func(s *Service) { s.newID = gen } }

// WithPresenceTTL sets how recently a member must have been seen to count as
// present in the presence display.
func WithPresenceTTL(d time.Duration) Option { return func(s *Service) { s.presenceTTL = d } }

// WithLogger sets the logger used for best-effort failures (e.g. bus publish).
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// WithTaskLease sets how long a task claim is held before it can be stolen by
// another agent (work-stealing on a dead assignee).
func WithTaskLease(d time.Duration) Option { return func(s *Service) { s.taskLease = d } }

// WithAckVisibility sets how long an ack-mode inbox lease lasts before an
// unacknowledged message is redelivered.
func WithAckVisibility(d time.Duration) Option { return func(s *Service) { s.ackVisibility = d } }

// WithRateLimits enables per-principal rate limiting on send, broadcast and
// publish_event. Zero rates leave an operation unlimited (the default).
func WithRateLimits(rl RateLimits) Option {
	return func(s *Service) { s.rl = newLimiter(rl, func() time.Time { return s.now() }) }
}

// WithImplicitRooms controls whether joining a non-existent room auto-creates
// it (open). True preserves the pre-v0.2 behaviour and keeps the zero-setup
// demo working; false means rooms must be created explicitly with RoomCreate.
func WithImplicitRooms(v bool) Option { return func(s *Service) { s.implicitRoom = v } }

// New constructs a Service over the given store and bus.
func New(st store.Store, b bus.Bus, opts ...Option) *Service {
	s := &Service{
		store:         st,
		bus:           b,
		now:           time.Now,
		newID:         func() string { return uuid.NewString() },
		presenceTTL:   defaultPresenceTTL,
		taskLease:     defaultTaskLease,
		ackVisibility: defaultAckVisibility,
		implicitRoom:  true, // default preserves pre-v0.2 behaviour
		log:           slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if s.rl == nil {
		// Limiting off by default: existing deployments and the demo are
		// unaffected until budgets are configured.
		s.rl = newLimiter(RateLimits{}, func() time.Time { return s.now() })
	}
	return s
}

// Join registers (or refreshes) a member and returns the stored record.
// Invite-only rooms reject a bare Join — use JoinWithInvite with a code.
func (s *Service) Join(ctx context.Context, workspace, name string, kind model.Kind, agentCard json.RawMessage) (model.Member, error) {
	return s.joinInternal(ctx, workspace, name, kind, agentCard, false)
}

// joinInternal is the shared join path. viaInvite is true when the caller
// arrived through JoinWithInvite with an already-redeemed invite, which
// bypasses the room's invite-only join policy (but never the ban list).
func (s *Service) joinInternal(ctx context.Context, workspace, name string, kind model.Kind, agentCard json.RawMessage, viaInvite bool) (model.Member, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Member{}, err
	}
	if err := validName("name", name); err != nil {
		return model.Member{}, err
	}
	if !kind.Valid() {
		return model.Member{}, fmt.Errorf("%w: kind must be %q or %q", ErrInvalidInput, model.KindHuman, model.KindAgent)
	}
	// With auth on, a credential may only join as its own identity and kind —
	// an agent token joining as "human" would otherwise gain review authority.
	if err := auth.CheckActor(ctx, workspace, name); err != nil {
		return model.Member{}, err
	}
	if err := auth.CheckKind(ctx, kind); err != nil {
		return model.Member{}, err
	}
	if len(agentCard) > maxAgentCardSize {
		return model.Member{}, fmt.Errorf("%w: agent_card must be at most %d bytes", ErrInvalidInput, maxAgentCardSize)
	}
	// The room must exist and be open to join (implicit mode lazily creates it).
	room, err := s.openRoom(ctx, workspace)
	if err != nil {
		return model.Member{}, err
	}
	// Invite-only rooms admit only invite-carrying joins.
	if room.JoinPolicy == model.JoinInvite && !viaInvite {
		return model.Member{}, fmt.Errorf("%w: room %q is invite-only; join with an invite code", ErrInvalidInput, workspace)
	}
	if len(agentCard) > 0 && !json.Valid(agentCard) {
		return model.Member{}, fmt.Errorf("%w: agent_card is not valid JSON", ErrInvalidInput)
	}
	// A banned name cannot (re)join until a moderator lifts the ban.
	if _, err := s.store.GetBan(ctx, workspace, name); err == nil {
		return model.Member{}, fmt.Errorf("%w: %q", store.ErrBanned, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Member{}, err
	}

	now := s.now()
	m := model.Member{
		Workspace: workspace,
		Name:      name,
		Kind:      kind,
		AgentCard: agentCard,
		JoinedAt:  now,
		LastSeen:  now,
	}
	// The room's creator becomes its owner on join; everyone else is a member.
	// (UpsertMember preserves an already-assigned role on re-join.)
	if room.CreatedBy == name {
		m.Role = model.RoleOwner
	} else {
		m.Role = model.RoleMember
	}
	stored, err := s.store.UpsertMember(ctx, m)
	if err != nil {
		return model.Member{}, err
	}
	s.appendEvent(ctx, workspace, name, EventMemberJoined, map[string]any{"kind": kind, "role": stored.Role})
	return stored, nil
}

// Presence returns the members active within the presence TTL.
func (s *Service) Presence(ctx context.Context, workspace string) ([]model.Member, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	return s.store.ListActiveMembers(ctx, workspace, s.now().Add(-s.presenceTTL))
}

// SendMessage delivers a direct, point-to-point message from one member to
// another. Both principals must already be members.
func (s *Service) SendMessage(ctx context.Context, workspace, from, to, body string) (model.Message, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Message{}, err
	}
	if err := validName("from", from); err != nil {
		return model.Message{}, err
	}
	if err := validName("to", to); err != nil {
		return model.Message{}, err
	}
	if body == "" || len(body) > maxMessageBody {
		return model.Message{}, fmt.Errorf("%w: body must be 1-%d bytes", ErrInvalidInput, maxMessageBody)
	}
	// Any-to-any addressing implies two distinct parties; a self-addressed
	// direct message is almost always a mistake.
	if from == to {
		return model.Message{}, fmt.Errorf("%w: cannot send a direct message to yourself", ErrInvalidInput)
	}
	if err := auth.CheckActor(ctx, workspace, from); err != nil {
		return model.Message{}, err
	}
	if err := s.rl.allow(workspace, from, opSend); err != nil {
		return model.Message{}, err
	}
	if err := s.requireOpenRoom(ctx, workspace); err != nil {
		return model.Message{}, err
	}
	if err := s.requireMember(ctx, workspace, from); err != nil {
		return model.Message{}, err
	}
	if err := s.requireMember(ctx, workspace, to); err != nil {
		return model.Message{}, err
	}
	msg := model.Message{
		ID:        s.newID(),
		Workspace: workspace,
		Sender:    from,
		Kind:      model.MessageDirect,
		Body:      body,
		CreatedAt: s.now(),
	}
	if err := s.store.CreateMessage(ctx, msg, []string{to}); err != nil {
		return model.Message{}, err
	}
	s.touch(ctx, workspace, from)
	s.appendEvent(ctx, workspace, from, EventMessageSent, map[string]any{"message_id": msg.ID, "to": to})
	s.publish(ctx, subjInbox(workspace, to), msg)
	return msg, nil
}

// Broadcast fans a message out to every member of the workspace except the
// sender. It returns the message and the number of recipients.
func (s *Service) Broadcast(ctx context.Context, workspace, from, body string) (model.Message, int, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Message{}, 0, err
	}
	if err := validName("from", from); err != nil {
		return model.Message{}, 0, err
	}
	if body == "" || len(body) > maxMessageBody {
		return model.Message{}, 0, fmt.Errorf("%w: body must be 1-%d bytes", ErrInvalidInput, maxMessageBody)
	}
	if err := auth.CheckActor(ctx, workspace, from); err != nil {
		return model.Message{}, 0, err
	}
	if err := s.rl.allow(workspace, from, opBroadcast); err != nil {
		return model.Message{}, 0, err
	}
	room, err := s.openRoom(ctx, workspace)
	if err != nil {
		return model.Message{}, 0, err
	}
	if err := s.requireMember(ctx, workspace, from); err != nil {
		return model.Message{}, 0, err
	}
	// Policy 'moderators' restricts fan-out to the human owner/moderators.
	// requireModerator demands a HUMAN, so moderator-role agents still cannot
	// broadcast under this policy — that is intentional: the policy exists so
	// humans can silence noisy agent loops.
	if room.WhoMayBroadcast == model.BroadcastModerators {
		if _, err := s.requireModerator(ctx, workspace, from); err != nil {
			return model.Message{}, 0, fmt.Errorf("broadcast restricted to moderators in room %q: %w", workspace, err)
		}
	}
	members, err := s.store.ListMembers(ctx, workspace)
	if err != nil {
		return model.Message{}, 0, err
	}
	recipients := make([]string, 0, len(members))
	for _, m := range members {
		if m.Name != from {
			recipients = append(recipients, m.Name)
		}
	}
	msg := model.Message{
		ID:        s.newID(),
		Workspace: workspace,
		Sender:    from,
		Kind:      model.MessageBroadcast,
		Body:      body,
		CreatedAt: s.now(),
	}
	if err := s.store.CreateMessage(ctx, msg, recipients); err != nil {
		return model.Message{}, 0, err
	}
	s.touch(ctx, workspace, from)
	s.appendEvent(ctx, workspace, from, EventBroadcast, map[string]any{"message_id": msg.ID, "recipients": len(recipients)})
	s.publish(ctx, subjEvents(workspace), msg)
	return msg, len(recipients), nil
}

// ReadInbox returns and consumes a member's undelivered messages.
func (s *Service) ReadInbox(ctx context.Context, workspace, member string) ([]model.Message, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := validName("member", member); err != nil {
		return nil, err
	}
	// The inbox is the most sensitive read path: only the member itself may
	// drain its messages.
	if err := auth.CheckActor(ctx, workspace, member); err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, workspace, member); err != nil {
		return nil, err
	}
	msgs, err := s.store.ReadInbox(ctx, workspace, member, s.now())
	if err != nil {
		return nil, err
	}
	s.annotateSenderKinds(ctx, workspace, msgs)
	s.touch(ctx, workspace, member)
	return msgs, nil
}

// annotateSenderKinds tags each message with its sender's kind (human/agent) —
// the LLM-tagging trust signal: receivers treat bodies as data, and the tag
// says who the data came from. Best-effort: a sender removed since writing
// simply yields no kind. Lookups are cached per sender.
func (s *Service) annotateSenderKinds(ctx context.Context, workspace string, msgs []model.Message) {
	kinds := make(map[string]model.Kind)
	for i := range msgs {
		k, ok := kinds[msgs[i].Sender]
		if !ok {
			if m, err := s.store.GetMember(ctx, workspace, msgs[i].Sender); err == nil {
				k = m.Kind
			}
			kinds[msgs[i].Sender] = k
		}
		msgs[i].SenderKind = k
	}
}

// PublishEvent appends a caller-defined event to the observation log.
func (s *Service) PublishEvent(ctx context.Context, workspace, source, eventType string, payload json.RawMessage) (model.Event, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Event{}, err
	}
	if err := validName("source", source); err != nil {
		return model.Event{}, err
	}
	if err := auth.CheckActor(ctx, workspace, source); err != nil {
		return model.Event{}, err
	}
	if err := s.rl.allow(workspace, source, opEvent); err != nil {
		return model.Event{}, err
	}
	if eventType == "" || len(eventType) > 128 {
		return model.Event{}, fmt.Errorf("%w: type must be 1-128 characters", ErrInvalidInput)
	}
	if len(payload) > maxEventPayload {
		return model.Event{}, fmt.Errorf("%w: payload must be at most %d bytes", ErrInvalidInput, maxEventPayload)
	}
	if len(payload) > 0 && !json.Valid(payload) {
		return model.Event{}, fmt.Errorf("%w: payload is not valid JSON", ErrInvalidInput)
	}
	if err := s.requireMember(ctx, workspace, source); err != nil {
		return model.Event{}, err
	}
	e, err := s.store.AppendEvent(ctx, model.Event{
		Workspace: workspace,
		Source:    source,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: s.now(),
	})
	if err != nil {
		return model.Event{}, err
	}
	s.touch(ctx, workspace, source)
	s.publish(ctx, subjEvents(workspace), e)
	return e, nil
}

// Subscribe returns events after the given cursor and the new cursor to use on
// the next poll. If member is non-empty it is treated as the polling member and
// its presence heartbeat is refreshed.
func (s *Service) Subscribe(ctx context.Context, workspace, member string, since int64, limit int) ([]model.Event, int64, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, since, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return nil, since, err
	}
	if member != "" {
		if err := validName("member", member); err != nil {
			return nil, since, err
		}
		if err := auth.CheckActor(ctx, workspace, member); err != nil {
			return nil, since, err
		}
		if err := s.requireMember(ctx, workspace, member); err != nil {
			return nil, since, err
		}
	}
	switch {
	case limit <= 0:
		limit = defaultEventLimit
	case limit > maxEventLimit:
		limit = maxEventLimit
	}
	events, err := s.store.EventsSince(ctx, workspace, since, limit)
	if err != nil {
		return nil, since, err
	}
	cursor := since
	if n := len(events); n > 0 {
		cursor = events[n-1].Seq
	}
	if member != "" {
		s.touch(ctx, workspace, member)
	}
	return events, cursor, nil
}

// requireMember returns store.ErrNotFound if the named member is absent.
func (s *Service) requireMember(ctx context.Context, workspace, name string) error {
	if _, err := s.store.GetMember(ctx, workspace, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("member %q: %w", name, store.ErrNotFound)
		}
		return err
	}
	return nil
}

// touch refreshes a member's heartbeat. It is best-effort: a missing member
// (removed concurrently) or store hiccup is logged, never fatal.
func (s *Service) touch(ctx context.Context, workspace, name string) {
	if err := s.store.TouchMember(ctx, workspace, name, s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.WarnContext(ctx, "touch member failed", "workspace", workspace, "member", name, "err", err)
	}
}

// appendEvent records a system event best-effort: the episodic log is the
// observation path and must never fail a successful coordination action.
func (s *Service) appendEvent(ctx context.Context, workspace, source, eventType string, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		s.log.WarnContext(ctx, "marshal event payload failed", "type", eventType, "err", err)
		return
	}
	e, err := s.store.AppendEvent(ctx, model.Event{
		Workspace: workspace,
		Source:    source,
		Type:      eventType,
		Payload:   raw,
		CreatedAt: s.now(),
	})
	if err != nil {
		s.log.WarnContext(ctx, "append system event failed", "type", eventType, "err", err)
		return
	}
	s.publish(ctx, subjEvents(workspace), e)
}

// publish sends a notification to the bus best-effort.
func (s *Service) publish(ctx context.Context, subject string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.log.WarnContext(ctx, "marshal bus payload failed", "subject", subject, "err", err)
		return
	}
	if err := s.bus.Publish(ctx, subject, data); err != nil {
		s.log.WarnContext(ctx, "bus publish failed", "subject", subject, "err", err)
	}
}

func validName(field, v string) error {
	if !nameRe.MatchString(v) {
		return fmt.Errorf("%w: %s must match %s", ErrInvalidInput, field, nameRe.String())
	}
	return nil
}

func subjEvents(workspace string) string { return "workspace." + workspace + ".events" }
func subjInbox(workspace, agent string) string {
	return "workspace." + workspace + ".agent." + agent + ".inbox"
}
