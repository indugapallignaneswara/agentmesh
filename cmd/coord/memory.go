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

// cmdMemory dispatches the `memory` subcommand group.
func cmdMemory(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("memory requires a subcommand: write|search|queue|approve|reject")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "write":
		return cmdMemoryWrite(ctx, cl, out, rest)
	case "search":
		return cmdMemorySearch(ctx, cl, out, rest)
	case "queue":
		return cmdMemoryQueue(ctx, cl, out, rest)
	case "approve":
		return cmdMemoryReview(ctx, cl, out, rest, "approve")
	case "reject":
		return cmdMemoryReview(ctx, cl, out, rest, "reject")
	default:
		return fmt.Errorf("unknown memory subcommand %q", sub)
	}
}

func cmdMemoryWrite(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("memory write", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	author := stringFlag(fs, "author", "AGENTMESH_MEMBER", "", "writing member")
	scope := fs.String("scope", "private", "memory scope: private or shared (shared is review-gated)")
	source := fs.String("source", "", "provenance: where this fact came from")
	content := fs.String("content", "", "memory content (or pass as positional args)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	text := *content
	if text == "" {
		text = strings.Join(fs.Args(), " ")
	}
	a := map[string]any{"workspace": *ws, "author": *author, "scope": *scope, "content": text}
	if *source != "" {
		a["source"] = *source
	}
	raw, err := cl.Raw(ctx, "memory_write", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ ID, Scope, Status string }
		_ = json.Unmarshal(b, &r)
		if r.Status == "pending" {
			fmt.Fprintf(w, "submitted %s memory %s for review (pending approval)\n", r.Scope, r.ID)
			return
		}
		fmt.Fprintf(w, "stored %s memory %s\n", r.Scope, r.ID)
	})
	return nil
}

func cmdMemorySearch(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	requester := stringFlag(fs, "requester", "AGENTMESH_MEMBER", "", "searching member")
	limit := fs.Int("limit", 10, "max results")
	query := fs.String("query", "", "search query (or pass as positional args)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	q := *query
	if q == "" {
		q = strings.Join(fs.Args(), " ")
	}
	raw, err := cl.Raw(ctx, "memory_search", map[string]any{
		"workspace": *ws, "requester": *requester, "query": q, "limit": *limit,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count    int `json:"count"`
			Memories []struct {
				ID      string `json:"id"`
				Scope   string `json:"scope"`
				Content string `json:"content"`
				Source  string `json:"source"`
			} `json:"memories"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no matching memories)")
			return
		}
		fmt.Fprintf(w, "%d memory item(s):\n", r.Count)
		for _, m := range r.Memories {
			src := ""
			if m.Source != "" {
				src = " (source: " + m.Source + ")"
			}
			fmt.Fprintf(w, "  [%s] %s%s\n      %s\n", m.Scope, m.ID, src, m.Content)
		}
	})
	return nil
}

func cmdMemoryQueue(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("memory queue", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	reviewer := stringFlag(fs, "reviewer", "AGENTMESH_MEMBER", "", "human reviewer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "memory_queue", map[string]any{"workspace": *ws, "reviewer": *reviewer})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count    int `json:"count"`
			Memories []struct {
				ID        string `json:"id"`
				CreatedBy string `json:"created_by"`
				Source    string `json:"source"`
				Content   string `json:"content"`
			} `json:"memories"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(review queue is empty)")
			return
		}
		fmt.Fprintf(w, "%d pending submission(s):\n", r.Count)
		for _, m := range r.Memories {
			fmt.Fprintf(w, "  %s by %s (source: %s)\n      %s\n", m.ID, m.CreatedBy, m.Source, m.Content)
		}
	})
	return nil
}

func cmdMemoryReview(ctx context.Context, cl *client.Client, out *output, args []string, decision string) error {
	fs := flag.NewFlagSet("memory "+decision, flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	reviewer := stringFlag(fs, "reviewer", "AGENTMESH_MEMBER", "", "human reviewer")
	id := fs.String("id", "", "memory id")
	note := fs.String("note", "", "review note")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "memory_review", map[string]any{
		"workspace": *ws, "reviewer": *reviewer, "id": *id, "decision": decision, "note": *note,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ ID, Status string }
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "memory %s -> %s\n", r.ID, r.Status)
	})
	return nil
}
