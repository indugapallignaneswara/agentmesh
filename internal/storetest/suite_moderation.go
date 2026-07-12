package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

func testMemberRoleSetAndSurvivesRejoin(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "alice", base)

	promoted, err := s.SetMemberRole(ctx, "ws1", "alice", model.RoleModerator)
	mustNoErr(t, err)
	if promoted.Role != model.RoleModerator || promoted.Name != "alice" {
		t.Fatalf("promoted = %+v, want alice as moderator", promoted)
	}
	got, err := s.GetMember(ctx, "ws1", "alice")
	mustNoErr(t, err)
	if got.Role != model.RoleModerator {
		t.Fatalf("role = %s, want moderator", got.Role)
	}

	// A re-join (upsert) must NOT reset the promoted role.
	join(t, s, "ws1", "alice", base.Add(time.Hour))
	got, err = s.GetMember(ctx, "ws1", "alice")
	mustNoErr(t, err)
	if got.Role != model.RoleModerator {
		t.Fatalf("role after rejoin = %s, want moderator preserved", got.Role)
	}

	if _, err := s.SetMemberRole(ctx, "ws1", "ghost", model.RoleModerator); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("set role on missing err = %v, want ErrNotFound", err)
	}
}

func testRemoveMemberPurgesUndelivered(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "alice", base)
	join(t, s, "ws1", "bob", base)

	// Fan out a message to both; bob never reads his copy.
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m1", Workspace: "ws1", Sender: "carol",
		Kind: model.MessageBroadcast, Body: "hello", CreatedAt: base,
	}, []string{"alice", "bob"}))

	mustNoErr(t, s.RemoveMember(ctx, "ws1", "bob"))

	members, err := s.ListMembers(ctx, "ws1")
	mustNoErr(t, err)
	if names := memberNames(members); len(names) != 1 || names[0] != "alice" {
		t.Fatalf("members after remove = %v, want [alice]", names)
	}

	// Re-joining under the same name must start with an empty inbox: the
	// removed member's undelivered deliveries were purged.
	join(t, s, "ws1", "bob", base.Add(time.Hour))
	inbox, err := s.ReadInbox(ctx, "ws1", "bob", base.Add(time.Hour))
	mustNoErr(t, err)
	if len(inbox) != 0 {
		t.Fatalf("rejoined bob inbox = %v, want empty", messageIDs(inbox))
	}

	// Alice's delivery is untouched.
	inbox, err = s.ReadInbox(ctx, "ws1", "alice", base.Add(time.Hour))
	mustNoErr(t, err)
	if len(inbox) != 1 || inbox[0].ID != "m1" {
		t.Fatalf("alice inbox = %v, want [m1]", messageIDs(inbox))
	}

	if err := s.RemoveMember(ctx, "ws1", "ghost"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("remove missing err = %v, want ErrNotFound", err)
	}
}

func testBanLifecycle(t *testing.T, s store.Store) {
	ctx := context.Background()

	created, err := s.CreateBan(ctx, model.Ban{
		Workspace: "ws1", Name: "mallory", BannedBy: "alice", Reason: "spam", CreatedAt: base,
	})
	mustNoErr(t, err)
	if created.BannedBy != "alice" || created.Reason != "spam" {
		t.Fatalf("created = %+v", created)
	}
	got, err := s.GetBan(ctx, "ws1", "mallory")
	mustNoErr(t, err)
	if got.Name != "mallory" || got.BannedBy != "alice" || got.Reason != "spam" || !got.CreatedAt.Equal(base) {
		t.Fatalf("got = %+v", got)
	}

	// Re-banning upserts rather than failing.
	_, err = s.CreateBan(ctx, model.Ban{
		Workspace: "ws1", Name: "mallory", BannedBy: "bob", Reason: "worse spam", CreatedAt: base.Add(time.Hour),
	})
	mustNoErr(t, err)
	got, err = s.GetBan(ctx, "ws1", "mallory")
	mustNoErr(t, err)
	if got.BannedBy != "bob" || got.Reason != "worse spam" {
		t.Fatalf("after re-ban got = %+v, want refreshed fields", got)
	}

	// Workspace isolation: the ban exists only in ws1.
	if _, err := s.GetBan(ctx, "ws2", "mallory"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("cross-workspace get err = %v, want ErrNotFound", err)
	}

	// Lift the ban; a second lift is not-found.
	mustNoErr(t, s.RemoveBan(ctx, "ws1", "mallory"))
	if _, err := s.GetBan(ctx, "ws1", "mallory"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("get after remove err = %v, want ErrNotFound", err)
	}
	if err := s.RemoveBan(ctx, "ws1", "mallory"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("second remove err = %v, want ErrNotFound", err)
	}

	// ListBans is ordered by name and workspace-scoped.
	for _, b := range []model.Ban{
		{Workspace: "ws1", Name: "zed", BannedBy: "alice", CreatedAt: base},
		{Workspace: "ws1", Name: "ann", BannedBy: "alice", CreatedAt: base},
		{Workspace: "ws2", Name: "bee", BannedBy: "alice", CreatedAt: base},
	} {
		_, err := s.CreateBan(ctx, b)
		mustNoErr(t, err)
	}
	bans, err := s.ListBans(ctx, "ws1")
	mustNoErr(t, err)
	if len(bans) != 2 || bans[0].Name != "ann" || bans[1].Name != "zed" {
		t.Fatalf("ws1 bans = %v, want [ann zed]", banNames(bans))
	}
}

func testListMessagesPaging(t *testing.T, s store.Store) {
	ctx := context.Background()
	join(t, s, "ws1", "bob", base)

	// Insert out of chronological order; listing must be oldest-first.
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m3", Workspace: "ws1", Sender: "alice", Kind: model.MessageDirect,
		Body: "third", CreatedAt: base.Add(3 * time.Minute),
	}, []string{"bob"}))
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m1", Workspace: "ws1", Sender: "alice", Kind: model.MessageDirect,
		Body: "first", CreatedAt: base.Add(1 * time.Minute),
	}, []string{"bob"}))
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "m2", Workspace: "ws1", Sender: "alice", Kind: model.MessageDirect,
		Body: "second", CreatedAt: base.Add(2 * time.Minute),
	}, []string{"bob"}))
	// A message in another room must never leak in.
	mustNoErr(t, s.CreateMessage(ctx, model.Message{
		ID: "other", Workspace: "ws2", Sender: "zoe", Kind: model.MessageDirect,
		Body: "elsewhere", CreatedAt: base,
	}, nil))

	all, err := s.ListMessages(ctx, "ws1", "", 0)
	mustNoErr(t, err)
	if ids := messageIDs(all); len(ids) != 3 || ids[0] != "m1" || ids[1] != "m2" || ids[2] != "m3" {
		t.Fatalf("all = %v, want [m1 m2 m3]", ids)
	}

	// Paging: page 1 of 2, then continue after its last id — no overlap, no gap.
	page1, err := s.ListMessages(ctx, "ws1", "", 2)
	mustNoErr(t, err)
	if ids := messageIDs(page1); len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("page1 = %v, want [m1 m2]", ids)
	}
	page2, err := s.ListMessages(ctx, "ws1", page1[len(page1)-1].ID, 2)
	mustNoErr(t, err)
	if ids := messageIDs(page2); len(ids) != 1 || ids[0] != "m3" {
		t.Fatalf("page2 = %v, want [m3]", ids)
	}

	// An unknown afterID pages from the start.
	fromStart, err := s.ListMessages(ctx, "ws1", "ghost", 0)
	mustNoErr(t, err)
	if ids := messageIDs(fromStart); len(ids) != 3 || ids[0] != "m1" {
		t.Fatalf("unknown afterID = %v, want full list from start", ids)
	}

	// Non-consuming: a second identical call returns the same rows...
	again, err := s.ListMessages(ctx, "ws1", "", 0)
	mustNoErr(t, err)
	if a, b := messageIDs(all), messageIDs(again); len(a) != len(b) || a[0] != b[0] || a[2] != b[2] {
		t.Fatalf("second list = %v, want same as first %v", b, a)
	}
	// ...and listing never marks deliveries, so ReadInbox still sees everything.
	inbox, err := s.ReadInbox(ctx, "ws1", "bob", base.Add(time.Hour))
	mustNoErr(t, err)
	if ids := messageIDs(inbox); len(ids) != 3 || ids[0] != "m1" || ids[2] != "m3" {
		t.Fatalf("inbox after listing = %v, want [m1 m2 m3]", ids)
	}
}

func banNames(bs []model.Ban) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Name
	}
	return out
}
