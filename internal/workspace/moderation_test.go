package workspace_test

import (
	"context"
	"errors"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// setupRoom creates an owned room with the given members joined. The creator is
// a human who becomes owner on join.
func setupModRoom(t *testing.T) (*workspace.Service, context.Context) {
	t.Helper()
	svc := newExplicitService(t)
	ctx := context.Background()
	if _, err := svc.RoomCreate(ctx, "team", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "owner", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	return svc, ctx
}

func TestOwnerRoleGrantedOnJoin(t *testing.T) {
	svc, ctx := setupModRoom(t)
	present, err := svc.Presence(ctx, "team")
	if err != nil {
		t.Fatal(err)
	}
	if len(present) != 1 || present[0].Role != model.RoleOwner {
		t.Fatalf("creator role = %+v, want owner", present)
	}
}

func TestKickRemovesMemberAndPurgesInbox(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	// A message is waiting for bot.
	if _, err := svc.SendMessage(ctx, "team", "owner", "bot", "hi"); err != nil {
		t.Fatal(err)
	}
	// Owner kicks bot.
	if err := svc.RoomKick(ctx, "team", "owner", "bot"); err != nil {
		t.Fatalf("kick: %v", err)
	}
	// bot is gone.
	present, _ := svc.Presence(ctx, "team")
	for _, m := range present {
		if m.Name == "bot" {
			t.Fatal("bot still present after kick")
		}
	}
	// Re-join as the same name: the purged message must NOT reappear.
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	msgs, err := svc.ReadInbox(ctx, "team", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("kicked member's purged inbox = %+v, want empty", msgs)
	}
}

func TestAgentCannotModerate(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "victim", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.RoomKick(ctx, "team", "bot", "victim"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("agent kick err = %v, want ErrInvalidInput", err)
	}
}

func TestPlainMemberCannotModerate(t *testing.T) {
	svc, ctx := setupModRoom(t)
	// A second human joins as a plain member (not the creator).
	if _, err := svc.Join(ctx, "team", "human2", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	// human2 is a member, not owner/moderator -> cannot kick.
	if err := svc.RoomKick(ctx, "team", "human2", "bot"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("member kick err = %v, want ErrInvalidInput", err)
	}
	// Owner promotes human2 to moderator; now it can.
	if _, err := svc.RoomSetRole(ctx, "team", "owner", "human2", model.RoleModerator); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if err := svc.RoomKick(ctx, "team", "human2", "bot"); err != nil {
		t.Fatalf("moderator kick after promotion: %v", err)
	}
}

func TestBanBlocksRejoin(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "bad", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoomBan(ctx, "team", "owner", "bad", "spamming"); err != nil {
		t.Fatalf("ban: %v", err)
	}
	// Banned name cannot rejoin.
	if _, err := svc.Join(ctx, "team", "bad", model.KindAgent, nil); !errors.Is(err, store.ErrBanned) {
		t.Fatalf("banned rejoin err = %v, want ErrBanned", err)
	}
	// Unban -> can join again.
	if err := svc.RoomUnban(ctx, "team", "owner", "bad"); err != nil {
		t.Fatalf("unban: %v", err)
	}
	if _, err := svc.Join(ctx, "team", "bad", model.KindAgent, nil); err != nil {
		t.Fatalf("rejoin after unban: %v", err)
	}
}

func TestCannotKickOwner(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "mod", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoomSetRole(ctx, "team", "owner", "mod", model.RoleModerator); err != nil {
		t.Fatal(err)
	}
	// A moderator cannot kick the owner.
	if err := svc.RoomKick(ctx, "team", "mod", "owner"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("kick owner err = %v, want ErrInvalidInput", err)
	}
}

func TestOnlyOwnerSetsRoles(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "mod", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "other", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RoomSetRole(ctx, "team", "owner", "mod", model.RoleModerator); err != nil {
		t.Fatal(err)
	}
	// A moderator (not owner) cannot set roles.
	if _, err := svc.RoomSetRole(ctx, "team", "mod", "other", model.RoleModerator); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("mod set role err = %v, want ErrInvalidInput", err)
	}
}

func TestLeaveRemovesSelf(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if err := svc.Leave(ctx, "team", "bot"); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := svc.SendMessage(ctx, "team", "owner", "bot", "still there?"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("send to left member err = %v, want ErrNotFound", err)
	}
	// Leaving when not a member is ErrNotFound.
	if err := svc.Leave(ctx, "team", "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("leave non-member err = %v, want ErrNotFound", err)
	}
}

func TestMessageHistoryHumanOnlyAndNonConsuming(t *testing.T) {
	svc, ctx := setupModRoom(t)
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, "team", "owner", "bot", "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, "team", "bot", "owner", "two"); err != nil {
		t.Fatal(err)
	}

	// An agent cannot read history.
	if _, err := svc.MessageHistory(ctx, "team", "bot", "", 50); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("agent history err = %v, want ErrInvalidInput", err)
	}
	// The human owner sees both messages, sender kinds tagged...
	h, err := svc.MessageHistory(ctx, "team", "owner", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].Body != "one" || h[1].Body != "two" {
		t.Fatalf("history = %+v, want [one two] oldest-first", h)
	}
	if h[0].SenderKind != model.KindHuman || h[1].SenderKind != model.KindAgent {
		t.Fatalf("history sender kinds = %v/%v", h[0].SenderKind, h[1].SenderKind)
	}
	// ...and history is non-consuming: bot's inbox still has its message.
	inbox, err := svc.ReadInbox(ctx, "team", "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Body != "one" {
		t.Fatalf("inbox after history = %+v, want [one] (history is non-consuming)", inbox)
	}
}
