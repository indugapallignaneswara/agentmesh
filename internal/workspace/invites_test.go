package workspace_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// setupInviteRoom builds an explicit-rooms service with a controllable clock
// and an owned room "team" whose human owner has joined. Advance the returned
// clock pointer to move time (e.g. past an invite's expiry).
func setupInviteRoom(t *testing.T) (*workspace.Service, context.Context, *time.Time) {
	t.Helper()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc := workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithImplicitRooms(false),
		workspace.WithClock(func() time.Time { return now }))
	ctx := context.Background()
	if _, err := svc.RoomCreate(ctx, "team", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "owner", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	return svc, ctx, &now
}

func TestInviteOnlyRoomRejectsBareJoin(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinInvite, model.BroadcastAnyone); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	_, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil)
	if !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("bare join err = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "invite-only") {
		t.Fatalf("bare join err = %q, want a readable invite-only message", err)
	}
}

func TestJoinWithInviteBurnsUse(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinInvite, model.BroadcastAnyone); err != nil {
		t.Fatal(err)
	}
	code, inv, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindAgent, "", 0, 0)
	if err != nil {
		t.Fatalf("invite create: %v", err)
	}
	if !strings.HasPrefix(code, "ami_") {
		t.Fatalf("code = %q, want ami_ prefix", code)
	}
	if inv.MaxUses != 1 || inv.Role != model.RoleMember || inv.Uses != 0 {
		t.Fatalf("invite = %+v, want default 1-use member invite", inv)
	}

	m, err := svc.JoinWithInvite(ctx, "team", "bot", model.KindAgent, nil, code)
	if err != nil {
		t.Fatalf("join with invite: %v", err)
	}
	if m.Role != model.RoleMember {
		t.Fatalf("joined role = %s, want member", m.Role)
	}
	// The use is burned and visible to moderators.
	invs, err := svc.RoomInvites(ctx, "team", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || invs[0].Uses != 1 {
		t.Fatalf("invites = %+v, want single invite with uses=1", invs)
	}
	// The exhausted code admits no one else.
	if _, err := svc.JoinWithInvite(ctx, "team", "bot2", model.KindAgent, nil, code); !errors.Is(err, store.ErrInviteSpent) {
		t.Fatalf("exhausted code err = %v, want ErrInviteSpent", err)
	}
}

func TestKindMismatchRejectedWithoutBurning(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	code, _, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindAgent, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// A human presenting an agent invite is rejected...
	if _, err := svc.JoinWithInvite(ctx, "team", "eve", model.KindHuman, nil, code); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("kind mismatch err = %v, want ErrInvalidInput", err)
	}
	// ...and no use was consumed.
	invs, err := svc.RoomInvites(ctx, "team", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || invs[0].Uses != 0 {
		t.Fatalf("invites = %+v, want uses=0 after mismatch", invs)
	}
	// A bogus code is not-found (and burns nothing).
	if _, err := svc.JoinWithInvite(ctx, "team", "bot", model.KindAgent, nil, "ami_bogus"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bogus code err = %v, want ErrNotFound", err)
	}
}

func TestRevokedAndExpiredCodesRejected(t *testing.T) {
	svc, ctx, now := setupInviteRoom(t)

	// Revoked.
	code, inv, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindAgent, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RoomInviteRevoke(ctx, "team", "owner", inv.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := svc.JoinWithInvite(ctx, "team", "bot", model.KindAgent, nil, code); !errors.Is(err, store.ErrInviteSpent) {
		t.Fatalf("revoked code err = %v, want ErrInviteSpent", err)
	}
	// Revoking an invite from another room is not-found.
	if _, err := svc.RoomCreate(ctx, "other", "owner2"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "other", "owner2", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	_, inv2, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindAgent, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RoomInviteRevoke(ctx, "other", "owner2", inv2.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-room revoke err = %v, want ErrNotFound", err)
	}

	// Expired: valid for 1h, presented 2h later.
	expCode, _, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindAgent, "", 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Hour)
	if _, err := svc.JoinWithInvite(ctx, "team", "late", model.KindAgent, nil, expCode); !errors.Is(err, store.ErrInviteSpent) {
		t.Fatalf("expired code err = %v, want ErrInviteSpent", err)
	}
}

func TestModeratorInviteGrantsModeratorOnJoin(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	code, _, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindHuman, model.RoleModerator, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.JoinWithInvite(ctx, "team", "helper", model.KindHuman, nil, code)
	if err != nil {
		t.Fatalf("join with moderator invite: %v", err)
	}
	if m.Role != model.RoleModerator {
		t.Fatalf("joined role = %s, want moderator", m.Role)
	}
	// The arrival really has moderator authority (moderator-only call works).
	if _, err := svc.RoomInvites(ctx, "team", "helper"); err != nil {
		t.Fatalf("promoted member cannot list invites: %v", err)
	}

	// Owner-role invites cannot be minted.
	if _, _, err := svc.RoomInviteCreate(ctx, "team", "owner", model.KindHuman, model.RoleOwner, 0, 0); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("owner-role invite err = %v, want ErrInvalidInput", err)
	}
}

func TestInviteCreateRequiresModerator(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	if _, err := svc.Join(ctx, "team", "human2", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	// A plain member cannot mint, list or revoke invites, nor set policy.
	if _, _, err := svc.RoomInviteCreate(ctx, "team", "human2", model.KindAgent, "", 0, 0); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("member mint err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.RoomInvites(ctx, "team", "bot"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("agent list err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.RoomSetPolicy(ctx, "team", "human2", model.JoinInvite, model.BroadcastAnyone); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("member set policy err = %v, want ErrInvalidInput", err)
	}
}

func TestBroadcastPolicyModerators(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	if _, err := svc.Join(ctx, "team", "human2", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinOpen, model.BroadcastModerators); err != nil {
		t.Fatal(err)
	}
	// Agents and plain members are blocked...
	if _, _, err := svc.Broadcast(ctx, "team", "bot", "spam"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("agent broadcast err = %v, want ErrInvalidInput", err)
	}
	if _, _, err := svc.Broadcast(ctx, "team", "human2", "me too"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("member broadcast err = %v, want ErrInvalidInput", err)
	}
	// ...the owner is not.
	if _, n, err := svc.Broadcast(ctx, "team", "owner", "announcement"); err != nil || n != 2 {
		t.Fatalf("owner broadcast = (%d, %v), want 2 recipients", n, err)
	}
	// Direct messages are unaffected by the broadcast policy.
	if _, err := svc.SendMessage(ctx, "team", "bot", "owner", "dm still fine"); err != nil {
		t.Fatalf("direct message under moderators policy: %v", err)
	}
	// Back to anyone -> the agent may broadcast again.
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinOpen, model.BroadcastAnyone); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Broadcast(ctx, "team", "bot", "unblocked"); err != nil {
		t.Fatalf("agent broadcast after policy reset: %v", err)
	}
}

func TestRoomPolicyRoundtripAndValidation(t *testing.T) {
	svc, ctx, _ := setupInviteRoom(t)
	w, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinInvite, model.BroadcastModerators)
	if err != nil {
		t.Fatal(err)
	}
	if w.JoinPolicy != model.JoinInvite || w.WhoMayBroadcast != model.BroadcastModerators {
		t.Fatalf("policy = %q/%q, want invite/moderators", w.JoinPolicy, w.WhoMayBroadcast)
	}
	rooms, err := svc.RoomList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].JoinPolicy != model.JoinInvite || rooms[0].WhoMayBroadcast != model.BroadcastModerators {
		t.Fatalf("listed rooms = %+v, want persisted invite/moderators", rooms)
	}
	// Bad enums are rejected.
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", "sesame", model.BroadcastAnyone); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("bad join policy err = %v, want ErrInvalidInput", err)
	}
	if _, err := svc.RoomSetPolicy(ctx, "team", "owner", model.JoinOpen, "nobody"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("bad broadcast policy err = %v, want ErrInvalidInput", err)
	}
}
