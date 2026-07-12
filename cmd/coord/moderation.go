package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdModerate dispatches the `mod` subcommand group (moderation actions).
func cmdModerate(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mod requires a subcommand: kick|ban|unban|bans|role")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "kick":
		return cmdModTarget(ctx, cl, out, rest, "room_kick", "kicked")
	case "ban":
		return cmdModBan(ctx, cl, out, rest)
	case "unban":
		return cmdModTarget(ctx, cl, out, rest, "room_unban", "unbanned")
	case "bans":
		return cmdModBans(ctx, cl, out, rest)
	case "role":
		return cmdModRole(ctx, cl, out, rest)
	default:
		return fmt.Errorf("unknown mod subcommand %q", sub)
	}
}

// cmdModTarget handles the simple {workspace, actor, target} moderation tools.
func cmdModTarget(ctx context.Context, cl *client.Client, out *output, args []string, tool, verb string) error {
	fs := flag.NewFlagSet("mod "+tool, flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	target := fs.String("target", "", "member name the action applies to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, tool, map[string]any{"workspace": *ws, "actor": *actor, "target": *target})
	if err != nil {
		return err
	}
	tgt := *target
	out.emit(raw, func(w io.Writer, b []byte) {
		fmt.Fprintf(w, "%s %s\n", verb, tgt)
	})
	return nil
}

func cmdModBan(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("mod ban", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	target := fs.String("target", "", "member name to ban")
	reason := fs.String("reason", "", "optional reason recorded with the ban")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "actor": *actor, "target": *target}
	if *reason != "" {
		a["reason"] = *reason
	}
	raw, err := cl.Raw(ctx, "room_ban", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Name     string `json:"name"`
			BannedBy string `json:"banned_by"`
			Reason   string `json:"reason"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Reason != "" {
			fmt.Fprintf(w, "banned %s (by %s): %s\n", r.Name, r.BannedBy, r.Reason)
			return
		}
		fmt.Fprintf(w, "banned %s (by %s)\n", r.Name, r.BannedBy)
	})
	return nil
}

func cmdModBans(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("mod bans", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_bans", map[string]any{"workspace": *ws, "actor": *actor})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count int `json:"count"`
			Bans  []struct {
				Name     string `json:"name"`
				BannedBy string `json:"banned_by"`
				Reason   string `json:"reason"`
			} `json:"bans"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no bans)")
			return
		}
		fmt.Fprintf(w, "%d ban(s):\n", r.Count)
		for _, bn := range r.Bans {
			line := fmt.Sprintf("  %s (by %s)", bn.Name, bn.BannedBy)
			if bn.Reason != "" {
				line += ": " + bn.Reason
			}
			fmt.Fprintln(w, line)
		}
	})
	return nil
}

func cmdModRole(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("mod role", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "room owner")
	target := fs.String("target", "", "member whose role to change")
	role := fs.String("role", "", "new role: moderator or member")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_set_role", map[string]any{
		"workspace": *ws, "actor": *actor, "target": *target, "role": *role,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "%s is now %s\n", r.Name, r.Role)
	})
	return nil
}

// cmdLeave removes the caller from the room (self-service departure).
func cmdLeave(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("leave", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "member leaving the room")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "workspace_leave", map[string]any{"workspace": *ws, "actor": *actor})
	if err != nil {
		return err
	}
	who, room := *actor, *ws
	out.emit(raw, func(w io.Writer, b []byte) {
		fmt.Fprintf(w, "%s left %s\n", who, room)
	})
	return nil
}

// cmdHistory reads the room's message log (human-only, non-consuming).
func cmdHistory(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	viewer := stringFlag(fs, "viewer", "AGENTMESH_MEMBER", "", "human member reviewing history")
	after := fs.String("after", "", "return messages after this message id")
	limit := fs.Int("limit", 0, "maximum messages to return (default 50, max 200)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "viewer": *viewer}
	if *after != "" {
		a["after_id"] = *after
	}
	if *limit > 0 {
		a["limit"] = *limit
	}
	raw, err := cl.Raw(ctx, "message_history", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count    int `json:"count"`
			Messages []struct {
				Sender     string `json:"sender"`
				SenderKind string `json:"sender_kind"`
				Body       string `json:"body"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no messages)")
			return
		}
		fmt.Fprintf(w, "%d message(s):\n", r.Count)
		for _, m := range r.Messages {
			kind := m.SenderKind
			if kind == "" {
				kind = "unknown"
			}
			fmt.Fprintf(w, "[%s] %s: %s\n", kind, m.Sender, m.Body)
		}
	})
	return nil
}
