package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdInvite dispatches the `invite` subcommand group (room invites).
func cmdInvite(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("invite requires a subcommand: create|list|revoke")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return cmdInviteCreate(ctx, cl, out, rest)
	case "list":
		return cmdInviteList(ctx, cl, out, rest)
	case "revoke":
		return cmdInviteRevoke(ctx, cl, out, rest)
	default:
		return fmt.Errorf("unknown invite subcommand %q", sub)
	}
}

func cmdInviteCreate(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("invite create", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator minting the invite")
	kind := fs.String("kind", "agent", "invitee kind the code admits: human or agent")
	role := fs.String("role", "", "role granted on join: member (default) or moderator")
	maxUses := fs.Int("max-uses", 0, "how many joins the code admits (default 1, max 1000)")
	ttl := fs.Duration("ttl", 0, "how long the code stays valid, e.g. 24h (0 = no expiry)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "actor": *actor, "kind": *kind}
	if *role != "" {
		a["role"] = *role
	}
	if *maxUses > 0 {
		a["max_uses"] = *maxUses
	}
	if *ttl > 0 {
		a["ttl_seconds"] = int(*ttl / time.Second)
	}
	raw, err := cl.Raw(ctx, "room_invite_create", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Code   string `json:"code"`
			Invite struct {
				ID        string `json:"id"`
				Kind      string `json:"kind"`
				Role      string `json:"role"`
				MaxUses   int    `json:"max_uses"`
				ExpiresAt string `json:"expires_at"`
			} `json:"invite"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "invite %s created (%s joins as %s, max %d use(s)", r.Invite.ID, r.Invite.Kind, r.Invite.Role, r.Invite.MaxUses)
		if r.Invite.ExpiresAt != "" {
			fmt.Fprintf(w, ", expires %s", r.Invite.ExpiresAt)
		}
		fmt.Fprintln(w, ")")
		fmt.Fprintln(w, "")
		fmt.Fprintf(w, "  CODE: %s\n", r.Code)
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "STORE THIS CODE NOW — it is shown once and cannot be recovered.")
	})
	return nil
}

func cmdInviteList(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("invite list", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_invites", map[string]any{"workspace": *ws, "actor": *actor})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count   int `json:"count"`
			Invites []struct {
				ID        string `json:"id"`
				Kind      string `json:"kind"`
				Role      string `json:"role"`
				MaxUses   int    `json:"max_uses"`
				Uses      int    `json:"uses"`
				CreatedBy string `json:"created_by"`
				ExpiresAt string `json:"expires_at"`
				RevokedAt string `json:"revoked_at"`
			} `json:"invites"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no invites)")
			return
		}
		fmt.Fprintf(w, "%d invite(s):\n", r.Count)
		for _, inv := range r.Invites {
			line := fmt.Sprintf("  %s %s/%s %d/%d used (by %s)", inv.ID, inv.Kind, inv.Role, inv.Uses, inv.MaxUses, inv.CreatedBy)
			if inv.RevokedAt != "" {
				line += " REVOKED"
			} else if inv.ExpiresAt != "" {
				line += " expires " + inv.ExpiresAt
			}
			fmt.Fprintln(w, line)
		}
	})
	return nil
}

func cmdInviteRevoke(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("invite revoke", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	id := fs.String("id", "", "invite id to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "room_invite_revoke", map[string]any{
		"workspace": *ws, "actor": *actor, "id": *id,
	})
	if err != nil {
		return err
	}
	revoked := *id
	out.emit(raw, func(w io.Writer, b []byte) {
		fmt.Fprintf(w, "revoked %s\n", revoked)
	})
	return nil
}

// cmdRoomPolicy sets the room's join/broadcast policies.
func cmdRoomPolicy(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("room policy", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "room name")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "human moderator")
	joinPolicy := fs.String("join", "", "who may join: open or invite")
	broadcast := fs.String("broadcast", "", "who may broadcast: anyone or moderators")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *joinPolicy == "" || *broadcast == "" {
		return fmt.Errorf("room policy requires both --join (open|invite) and --broadcast (anyone|moderators)")
	}
	raw, err := cl.Raw(ctx, "room_set_policy", map[string]any{
		"workspace": *ws, "actor": *actor,
		"join_policy": *joinPolicy, "who_may_broadcast": *broadcast,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Name            string `json:"name"`
			JoinPolicy      string `json:"join_policy"`
			WhoMayBroadcast string `json:"who_may_broadcast"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "room %q policy: join=%s broadcast=%s\n", r.Name, r.JoinPolicy, r.WhoMayBroadcast)
	})
	return nil
}
