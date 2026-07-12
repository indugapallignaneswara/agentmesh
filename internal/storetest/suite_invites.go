package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// mkInvite creates an invite and fails the test on error.
func mkInvite(t *testing.T, s store.Store, inv model.Invite) model.Invite {
	t.Helper()
	got, err := s.CreateInvite(context.Background(), inv)
	mustNoErr(t, err)
	return got
}

func testInviteCreateGetByHash(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkInvite(t, s, model.Invite{
		ID: "inv_1", CodeHash: "hash1", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 3, CreatedBy: "alice", CreatedAt: base,
	})
	got, err := s.GetInviteByHash(ctx, "hash1")
	mustNoErr(t, err)
	if got.ID != "inv_1" || got.Workspace != "ws1" || got.Kind != model.KindAgent ||
		got.Role != model.RoleMember || got.MaxUses != 3 || got.Uses != 0 || got.CreatedBy != "alice" {
		t.Fatalf("got = %+v", got)
	}
	if _, err := s.GetInviteByHash(ctx, "ghost"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("get missing err = %v, want ErrNotFound", err)
	}

	// GetInviteByHash returns invites in ANY state: a revoked one is still visible.
	mustNoErr(t, s.RevokeInvite(ctx, "inv_1", base.Add(time.Hour)))
	got, err = s.GetInviteByHash(ctx, "hash1")
	mustNoErr(t, err)
	if got.RevokedAt == nil {
		t.Fatalf("revoked invite = %+v, want RevokedAt set", got)
	}
}

func testInviteRedeemAtomic(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkInvite(t, s, model.Invite{
		ID: "inv_multi", CodeHash: "hmulti", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 2, CreatedBy: "alice", CreatedAt: base,
	})

	// First redeem: uses 0 -> 1.
	r1, err := s.RedeemInvite(ctx, "hmulti", base.Add(time.Minute))
	mustNoErr(t, err)
	if r1.Uses != 1 {
		t.Fatalf("first redeem uses = %d, want 1", r1.Uses)
	}
	// Second redeem: uses 1 -> 2 (exhausted).
	r2, err := s.RedeemInvite(ctx, "hmulti", base.Add(2*time.Minute))
	mustNoErr(t, err)
	if r2.Uses != 2 {
		t.Fatalf("second redeem uses = %d, want 2", r2.Uses)
	}
	// Third redeem: spent.
	if _, err := s.RedeemInvite(ctx, "hmulti", base.Add(3*time.Minute)); !errorsIs(err, store.ErrInviteSpent) {
		t.Fatalf("exhausted redeem err = %v, want ErrInviteSpent", err)
	}
	// Uses must not have moved past max.
	got, err := s.GetInviteByHash(ctx, "hmulti")
	mustNoErr(t, err)
	if got.Uses != 2 {
		t.Fatalf("uses after failed redeem = %d, want 2", got.Uses)
	}

	// Expired invite: exists but not redeemable.
	exp := base.Add(time.Hour)
	mkInvite(t, s, model.Invite{
		ID: "inv_exp", CodeHash: "hexp", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 1, CreatedBy: "alice", CreatedAt: base, ExpiresAt: &exp,
	})
	if _, err := s.RedeemInvite(ctx, "hexp", exp.Add(time.Second)); !errorsIs(err, store.ErrInviteSpent) {
		t.Fatalf("expired redeem err = %v, want ErrInviteSpent", err)
	}
	// Exactly at the boundary the invite is no longer redeemable (expires_at > now fails).
	if _, err := s.RedeemInvite(ctx, "hexp", exp); !errorsIs(err, store.ErrInviteSpent) {
		t.Fatalf("boundary redeem err = %v, want ErrInviteSpent", err)
	}
	// Before expiry it works.
	r3, err := s.RedeemInvite(ctx, "hexp", base.Add(time.Minute))
	mustNoErr(t, err)
	if r3.Uses != 1 {
		t.Fatalf("pre-expiry redeem uses = %d, want 1", r3.Uses)
	}

	// Revoked invite: exists but not redeemable.
	mkInvite(t, s, model.Invite{
		ID: "inv_rev", CodeHash: "hrev", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 5, CreatedBy: "alice", CreatedAt: base,
	})
	mustNoErr(t, s.RevokeInvite(ctx, "inv_rev", base.Add(time.Minute)))
	if _, err := s.RedeemInvite(ctx, "hrev", base.Add(2*time.Minute)); !errorsIs(err, store.ErrInviteSpent) {
		t.Fatalf("revoked redeem err = %v, want ErrInviteSpent", err)
	}

	// Absent hash: not found (distinct from spent).
	if _, err := s.RedeemInvite(ctx, "ghost", base); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("absent redeem err = %v, want ErrNotFound", err)
	}
}

func testInviteListOrder(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkInvite(t, s, model.Invite{
		ID: "inv_old", CodeHash: "h1", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 1, CreatedBy: "alice", CreatedAt: base,
	})
	mkInvite(t, s, model.Invite{
		ID: "inv_new", CodeHash: "h2", Workspace: "ws1", Kind: model.KindHuman,
		Role: model.RoleModerator, MaxUses: 1, CreatedBy: "alice", CreatedAt: base.Add(time.Hour),
	})
	mkInvite(t, s, model.Invite{
		ID: "inv_other", CodeHash: "h3", Workspace: "ws2", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 1, CreatedBy: "zoe", CreatedAt: base,
	})

	got, err := s.ListInvites(ctx, "ws1")
	mustNoErr(t, err)
	if len(got) != 2 || got[0].ID != "inv_new" || got[1].ID != "inv_old" {
		t.Fatalf("list = %v, want [inv_new inv_old] (newest first)", inviteIDs(got))
	}
}

func testInviteRevoke(t *testing.T, s store.Store) {
	ctx := context.Background()
	mkInvite(t, s, model.Invite{
		ID: "inv_r", CodeHash: "hr", Workspace: "ws1", Kind: model.KindAgent,
		Role: model.RoleMember, MaxUses: 1, CreatedBy: "alice", CreatedAt: base,
	})
	mustNoErr(t, s.RevokeInvite(ctx, "inv_r", base.Add(time.Minute)))
	// Double revoke is not-found.
	if err := s.RevokeInvite(ctx, "inv_r", base.Add(2*time.Minute)); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("double revoke err = %v, want ErrNotFound", err)
	}
	// Revoking an absent id is not-found.
	if err := s.RevokeInvite(ctx, "ghost", base); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("revoke missing err = %v, want ErrNotFound", err)
	}
}

func testWorkspacePolicy(t *testing.T, s store.Store) {
	ctx := context.Background()

	// Defaults on lazily-ensured rooms: open / anyone.
	ensured, err := s.EnsureWorkspace(ctx, "auto", base)
	mustNoErr(t, err)
	if ensured.JoinPolicy != model.JoinOpen || ensured.WhoMayBroadcast != model.BroadcastAnyone {
		t.Fatalf("ensured policies = %q/%q, want open/anyone", ensured.JoinPolicy, ensured.WhoMayBroadcast)
	}
	// Defaults on explicitly-created rooms too.
	mustCreateRoom(t, s, "team", "alice")
	created, err := s.GetWorkspace(ctx, "team")
	mustNoErr(t, err)
	if created.JoinPolicy != model.JoinOpen || created.WhoMayBroadcast != model.BroadcastAnyone {
		t.Fatalf("created policies = %q/%q, want open/anyone", created.JoinPolicy, created.WhoMayBroadcast)
	}

	// Roundtrip a policy change.
	updated, err := s.SetWorkspacePolicy(ctx, "team", model.JoinInvite, model.BroadcastModerators, base.Add(time.Hour))
	mustNoErr(t, err)
	if updated.JoinPolicy != model.JoinInvite || updated.WhoMayBroadcast != model.BroadcastModerators {
		t.Fatalf("updated policies = %q/%q, want invite/moderators", updated.JoinPolicy, updated.WhoMayBroadcast)
	}
	if !updated.UpdatedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("UpdatedAt = %v, want bumped to %v", updated.UpdatedAt, base.Add(time.Hour))
	}
	reread, err := s.GetWorkspace(ctx, "team")
	mustNoErr(t, err)
	if reread.JoinPolicy != model.JoinInvite || reread.WhoMayBroadcast != model.BroadcastModerators {
		t.Fatalf("reread policies = %q/%q, want invite/moderators", reread.JoinPolicy, reread.WhoMayBroadcast)
	}

	// Missing room is not-found.
	if _, err := s.SetWorkspacePolicy(ctx, "ghost", model.JoinOpen, model.BroadcastAnyone, base); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("set policy on missing err = %v, want ErrNotFound", err)
	}
}

func inviteIDs(invs []model.Invite) []string {
	out := make([]string, len(invs))
	for i, inv := range invs {
		out[i] = inv.ID
	}
	return out
}
