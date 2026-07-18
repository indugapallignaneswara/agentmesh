package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdRoom dispatches the `room` subcommand group.
func cmdRoom(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("room requires a subcommand: create|close|reopen|list|policy|budget")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return cmdRoomCreate(ctx, cl, out, rest)
	case "close":
		return cmdRoomMod(ctx, cl, out, rest, "room_close")
	case "reopen":
		return cmdRoomMod(ctx, cl, out, rest, "room_reopen")
	case "list":
		return cmdRoomList(ctx, cl, out, rest)
	case "policy":
		return cmdRoomPolicy(ctx, cl, out, rest)
	case "budget":
		return cmdRoomBudget(ctx, cl, out, rest)
	default:
		return fmt.Errorf("unknown room subcommand %q", sub)
	}
}

func cmdRoomCreate(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("room create", flag.ContinueOnError)
	name := stringFlag(fs, "name", "AGENTMESH_WORKSPACE", "", "room name")
	creator := stringFlag(fs, "creator", "AGENTMESH_MEMBER", "", "human creating the room")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_create", map[string]any{"name": *name, "creator": *creator})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			CreatedBy string `json:"created_by"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "created room %q (%s, owner %s)\n", r.Name, r.Status, r.CreatedBy)
	})
	return nil
}

func cmdRoomMod(ctx context.Context, cl *client.Client, out *output, args []string, tool string) error {
	fs := flag.NewFlagSet("room "+tool, flag.ContinueOnError)
	name := stringFlag(fs, "name", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human member")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, tool, map[string]any{"name": *name, "actor": *actor})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ Name, Status string }
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "room %q -> %s\n", r.Name, r.Status)
	})
	return nil
}

// cmdRoomBudget sets the room's daily coordination-byte budgets for agents
// (0 = unlimited; humans are never budget-blocked).
func cmdRoomBudget(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("room budget", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	daily := fs.Int64("daily", 0, "room-wide daily byte budget for agent traffic (0 = unlimited)")
	memberDaily := fs.Int64("member-daily", 0, "per-agent daily byte cap (0 = unlimited)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_set_budget", map[string]any{
		"workspace": *ws, "actor": *actor,
		"daily_bytes": *daily, "member_daily_bytes": *memberDaily,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Name                   string `json:"name"`
			BudgetDailyBytes       int64  `json:"budget_daily_bytes"`
			BudgetMemberDailyBytes int64  `json:"budget_member_daily_bytes"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "room %q budget: daily=%d member-daily=%d (0 = unlimited)\n",
			r.Name, r.BudgetDailyBytes, r.BudgetMemberDailyBytes)
	})
	return nil
}

func cmdRoomList(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("room list", flag.ContinueOnError)
	status := fs.String("status", "", "comma-separated status filter: open,closed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{}
	if *status != "" {
		a["statuses"] = splitCSV(*status)
	}
	raw, err := cl.Raw(ctx, "room_list", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count int `json:"count"`
			Rooms []struct {
				Name      string `json:"name"`
				Status    string `json:"status"`
				CreatedBy string `json:"created_by"`
			} `json:"rooms"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no rooms)")
			return
		}
		fmt.Fprintf(w, "%d room(s):\n", r.Count)
		for _, rm := range r.Rooms {
			owner := ""
			if rm.CreatedBy != "" {
				owner = " owner=" + rm.CreatedBy
			}
			fmt.Fprintf(w, "  %s [%s]%s\n", rm.Name, rm.Status, owner)
		}
	})
	return nil
}
