package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdArtifact dispatches the `artifact` subcommand group.
func cmdArtifact(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("artifact requires a subcommand: get|put|list")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "get":
		return cmdArtifactGet(ctx, cl, out, rest)
	case "put":
		return cmdArtifactPut(ctx, cl, out, rest)
	case "list":
		return cmdArtifactList(ctx, cl, out, rest)
	default:
		return fmt.Errorf("unknown artifact subcommand %q", sub)
	}
}

func cmdArtifactGet(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("artifact get", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	name := fs.String("name", "", "artifact name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "get_artifact", map[string]any{"workspace": *ws, "name": *name})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var a struct {
			Name      string `json:"name"`
			Version   int64  `json:"version"`
			UpdatedBy string `json:"updated_by"`
			Content   string `json:"content"`
		}
		_ = json.Unmarshal(b, &a)
		fmt.Fprintf(w, "%s (version %d, last edited by %s)\n---\n%s\n", a.Name, a.Version, a.UpdatedBy, a.Content)
	})
	return nil
}

func cmdArtifactPut(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("artifact put", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	author := stringFlag(fs, "author", "AGENTMESH_MEMBER", "", "writing member")
	name := fs.String("name", "", "artifact name")
	base := fs.Int64("base-version", 0, "the version this edit is based on (0 to create)")
	content := fs.String("content", "", "full new content (or pass as positional args)")
	positional, err := parsePositional(fs, args)
	if err != nil {
		return err
	}
	text := *content
	if text == "" {
		text = positional
	}
	raw, err := cl.Raw(ctx, "update_artifact", map[string]any{
		"workspace": *ws, "author": *author, "name": *name,
		"content": text, "base_version": *base,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var a struct {
			Name    string `json:"name"`
			Version int64  `json:"version"`
		}
		_ = json.Unmarshal(b, &a)
		fmt.Fprintf(w, "wrote %s (now version %d)\n", a.Name, a.Version)
	})
	return nil
}

func cmdArtifactList(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("artifact list", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "list_artifacts", map[string]any{"workspace": *ws})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count     int `json:"count"`
			Artifacts []struct {
				Name      string `json:"name"`
				Version   int64  `json:"version"`
				UpdatedBy string `json:"updated_by"`
			} `json:"artifacts"`
		}
		_ = json.Unmarshal(b, &r)
		if r.Count == 0 {
			fmt.Fprintln(w, "(no artifacts)")
			return
		}
		fmt.Fprintf(w, "%d artifact(s):\n", r.Count)
		for _, a := range r.Artifacts {
			fmt.Fprintf(w, "  %s v%d (last: %s)\n", a.Name, a.Version, a.UpdatedBy)
		}
	})
	return nil
}
