package workspace_test

import (
	"context"
	"errors"
	"testing"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// newExplicitService builds a service with implicit-room creation OFF, so room
// lifecycle is exercised directly.
func newExplicitService(t *testing.T) *workspace.Service {
	t.Helper()
	return workspace.New(store.NewMemory(), bus.NewNoop(),
		workspace.WithImplicitRooms(false))
}

func TestRoomCreateAndList(t *testing.T) {
	svc := newExplicitService(t)
	ctx := context.Background()

	w, err := svc.RoomCreate(ctx, "team", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != model.WorkspaceOpen || w.CreatedBy != "alice" {
		t.Fatalf("created = %+v", w)
	}
	// Duplicate create is a conflict.
	if _, err := svc.RoomCreate(ctx, "team", "alice"); !errors.Is(err, store.ErrRoomExists) {
		t.Fatalf("dup create err = %v, want ErrRoomExists", err)
	}
	rooms, err := svc.RoomList(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || rooms[0].Name != "team" {
		t.Fatalf("list = %+v", rooms)
	}
}

// TestExplicitModeRejectsUnknownRoom proves that with implicit rooms off, you
// cannot join a room that was never created.
func TestExplicitModeRejectsUnknownRoom(t *testing.T) {
	svc := newExplicitService(t)
	ctx := context.Background()
	if _, err := svc.Join(ctx, "ghost", "alice", model.KindHuman, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("join unknown room err = %v, want ErrNotFound", err)
	}
	// After creating it, join works.
	if _, err := svc.RoomCreate(ctx, "real", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "real", "alice", model.KindHuman, nil); err != nil {
		t.Fatalf("join created room: %v", err)
	}
}

// TestClosedRoomRejectsWritesButAllowsReads is the core room-lifecycle
// guarantee: a closed room stays readable but rejects new content.
func TestClosedRoomRejectsWritesButAllowsReads(t *testing.T) {
	svc := newExplicitService(t)
	ctx := context.Background()

	if _, err := svc.RoomCreate(ctx, "team", "alice"); err != nil {
		t.Fatal(err)
	}
	// Seed while open.
	if _, err := svc.Join(ctx, "team", "alice", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, "team", "alice", "bot", "hi"); err != nil {
		t.Fatal(err)
	}

	// Close it (human member).
	closed, err := svc.RoomClose(ctx, "team", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != model.WorkspaceClosed {
		t.Fatalf("status = %s, want closed", closed.Status)
	}

	// Writes are now rejected...
	if _, err := svc.SendMessage(ctx, "team", "alice", "bot", "again"); !errors.Is(err, workspace.ErrRoomClosed) {
		t.Fatalf("send to closed err = %v, want ErrRoomClosed", err)
	}
	if _, err := svc.Join(ctx, "team", "late", model.KindAgent, nil); !errors.Is(err, workspace.ErrRoomClosed) {
		t.Fatalf("join closed err = %v, want ErrRoomClosed", err)
	}
	if _, err := svc.CreateTask(ctx, "team", "alice", "task", "", nil); !errors.Is(err, workspace.ErrRoomClosed) {
		t.Fatalf("create task in closed err = %v, want ErrRoomClosed", err)
	}

	// ...but reads still work: bot drains the message sent before closing.
	msgs, err := svc.ReadInbox(ctx, "team", "bot")
	if err != nil {
		t.Fatalf("read closed room inbox: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "hi" {
		t.Fatalf("inbox = %+v, want the pre-close message", msgs)
	}
	if _, err := svc.Presence(ctx, "team"); err != nil {
		t.Fatalf("presence on closed room: %v", err)
	}

	// Reopen -> writes flow again.
	if _, err := svc.RoomReopen(ctx, "team", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendMessage(ctx, "team", "alice", "bot", "back"); err != nil {
		t.Fatalf("send after reopen: %v", err)
	}
}

// TestRoomModerationRequiresHumanMember proves close/reopen authority is a
// human member of the room, not any agent.
func TestRoomModerationRequiresHumanMember(t *testing.T) {
	svc := newExplicitService(t)
	ctx := context.Background()
	if _, err := svc.RoomCreate(ctx, "team", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "bot", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	// An agent member cannot close the room.
	if _, err := svc.RoomClose(ctx, "team", "bot"); !errors.Is(err, workspace.ErrInvalidInput) {
		t.Fatalf("agent close err = %v, want ErrInvalidInput", err)
	}
	// A non-member cannot close it either.
	if _, err := svc.RoomClose(ctx, "team", "stranger"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-member close err = %v, want ErrNotFound", err)
	}
}
