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
		return fmt.Errorf("room requires a subcommand: create|close|reopen|list")
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
		var r struct{ Name, Status, CreatedBy string }
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
