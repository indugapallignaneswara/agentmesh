package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// parsePositional parses a flag set allowing flags to appear before, after, or
// interspersed with positional arguments, and returns the collected
// positionals joined by spaces. Go's flag package stops at the first
// non-flag token, which would silently ignore a trailing flag like
// `... "some text" --limit 1`; this loop peels positionals one at a time and
// keeps parsing, so `coord memory search "q" --limit 1` behaves the same as
// `coord memory search --limit 1 "q"`. Bool flags are handled correctly
// because the FlagSet knows each flag's arity.
func parsePositional(fs *flag.FlagSet, args []string) (string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return strings.Join(positional, " "), nil
}

// output renders results either as the server's raw JSON (--json, for scripts)
// or as a short human-readable summary (default, for interactive use).
type output struct {
	json bool
	w    io.Writer
}

// emit prints raw JSON verbatim in --json mode, otherwise calls human().
func (o *output) emit(raw string, human func(io.Writer, []byte)) {
	if o.json {
		fmt.Fprintln(o.w, strings.TrimRight(raw, "\n"))
		return
	}
	human(o.w, []byte(raw))
}

// --- join ---

func cmdJoin(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	name := stringFlag(fs, "name", "AGENTMESH_MEMBER", "", "member name")
	kind := fs.String("kind", "agent", "member kind: human or agent")
	card := fs.String("agent-card", "", "optional JSON capability/identity card")
	invite := fs.String("invite", "", "invite code for invite-only rooms")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "name": *name, "kind": *kind}
	if *invite != "" {
		a["invite_code"] = *invite
	}
	if *card != "" {
		var v any
		if err := json.Unmarshal([]byte(*card), &v); err != nil {
			return fmt.Errorf("--agent-card must be valid JSON: %w", err)
		}
		a["agent_card"] = v
	}
	raw, err := cl.Raw(ctx, "workspace_join", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var m struct{ Name, Kind, Workspace string }
		_ = json.Unmarshal(b, &m)
		fmt.Fprintf(w, "joined %q as %s (%s)\n", m.Workspace, m.Name, m.Kind)
	})
	return nil
}

// --- presence ---

func cmdPresence(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("presence", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "workspace_presence", map[string]any{"workspace": *ws})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count   int `json:"count"`
			Members []struct {
				Name string `json:"name"`
				Kind string `json:"kind"`
			} `json:"members"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "%d present:\n", r.Count)
		for _, m := range r.Members {
			fmt.Fprintf(w, "  - %s (%s)\n", m.Name, m.Kind)
		}
	})
	return nil
}

// --- send ---

func cmdSend(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	from := stringFlag(fs, "from", "AGENTMESH_MEMBER", "", "sender name")
	to := fs.String("to", "", "recipient name")
	body := fs.String("body", "", "message body (or pass as positional args)")
	positional, err := parsePositional(fs, args)
	if err != nil {
		return err
	}
	text := *body
	if text == "" {
		text = positional
	}
	raw, err := cl.Raw(ctx, "send_message", map[string]any{
		"workspace": *ws, "from": *from, "to": *to, "body": text,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		fmt.Fprintf(w, "sent to %s\n", *to)
	})
	return nil
}

// --- inbox ---

func cmdInbox(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	member := stringFlag(fs, "member", "AGENTMESH_MEMBER", "", "whose inbox to read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "read_inbox", map[string]any{"workspace": *ws, "member": *member})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count    int `json:"count"`
			Messages []struct {
				Sender string `json:"sender"`
				Kind   string `json:"kind"`
				Body   string `json:"body"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no new messages)")
			return
		}
		fmt.Fprintf(w, "%d new message(s):\n", r.Count)
		for _, m := range r.Messages {
			fmt.Fprintf(w, "  [%s] %s: %s\n", m.Kind, m.Sender, m.Body)
		}
	})
	return nil
}

// --- broadcast ---

func cmdBroadcast(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("broadcast", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	from := stringFlag(fs, "from", "AGENTMESH_MEMBER", "", "sender name")
	body := fs.String("body", "", "message body (or pass as positional args)")
	positional, err := parsePositional(fs, args)
	if err != nil {
		return err
	}
	text := *body
	if text == "" {
		text = positional
	}
	raw, err := cl.Raw(ctx, "broadcast", map[string]any{
		"workspace": *ws, "from": *from, "body": text,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Recipients int `json:"recipients"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "broadcast to %d member(s)\n", r.Recipients)
	})
	return nil
}

// --- publish ---

func cmdPublish(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	source := stringFlag(fs, "source", "AGENTMESH_MEMBER", "", "publishing member")
	typ := fs.String("type", "", "event type name")
	payload := fs.String("payload", "", "optional JSON payload")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "source": *source, "type": *typ}
	if *payload != "" {
		var v any
		if err := json.Unmarshal([]byte(*payload), &v); err != nil {
			return fmt.Errorf("--payload must be valid JSON: %w", err)
		}
		a["payload"] = v
	}
	raw, err := cl.Raw(ctx, "publish_event", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var e struct {
			Seq  int64  `json:"seq"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(b, &e)
		fmt.Fprintf(w, "published %q (seq %d)\n", e.Type, e.Seq)
	})
	return nil
}

// --- subscribe ---

func cmdSubscribe(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("subscribe", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	member := stringFlag(fs, "member", "AGENTMESH_MEMBER", "", "optional polling member (refreshes presence)")
	since := fs.Int64("since", 0, "return events after this cursor")
	limit := fs.Int("limit", 100, "max events")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws, "since": *since, "limit": *limit}
	if *member != "" {
		a["member"] = *member
	}
	raw, err := cl.Raw(ctx, "subscribe", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Cursor int64 `json:"cursor"`
			Count  int   `json:"count"`
			Events []struct {
				Seq    int64  `json:"seq"`
				Source string `json:"source"`
				Type   string `json:"type"`
			} `json:"events"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "%d event(s), cursor=%d:\n", r.Count, r.Cursor)
		for _, e := range r.Events {
			fmt.Fprintf(w, "  #%d %s: %s\n", e.Seq, e.Source, e.Type)
		}
	})
	return nil
}
