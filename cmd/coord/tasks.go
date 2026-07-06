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

// cmdTask dispatches the `task` subcommand group.
func cmdTask(ctx context.Context, cl *client.Client, out *output, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("task requires a subcommand: create|claim|complete|retry|get|list")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return cmdTaskCreate(ctx, cl, out, rest)
	case "claim":
		return cmdTaskClaim(ctx, cl, out, rest)
	case "complete":
		return cmdTaskComplete(ctx, cl, out, rest)
	case "retry":
		return cmdTaskRetry(ctx, cl, out, rest)
	case "get":
		return cmdTaskGet(ctx, cl, out, rest)
	case "list":
		return cmdTaskList(ctx, cl, out, rest)
	default:
		return fmt.Errorf("unknown task subcommand %q", sub)
	}
}

func cmdTaskRetry(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task retry", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	actor := stringFlag(fs, "actor", "AGENTMESH_MEMBER", "", "requesting member")
	id := fs.String("id", "", "failed task id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "retry_task", map[string]any{"workspace": *ws, "actor": *actor, "id": *id})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ ID, Status string }
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "task %s -> %s (requeued)\n", r.ID, r.Status)
	})
	return nil
}

func cmdTaskCreate(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	creator := stringFlag(fs, "creator", "AGENTMESH_MEMBER", "", "creating member")
	title := fs.String("title", "", "task title (or pass as positional args)")
	details := fs.String("details", "", "longer description")
	deps := fs.String("depends-on", "", "comma-separated task ids this depends on")
	positional, err := parsePositional(fs, args)
	if err != nil {
		return err
	}
	t := *title
	if t == "" {
		t = positional
	}
	a := map[string]any{"workspace": *ws, "creator": *creator, "title": t}
	if *details != "" {
		a["details"] = *details
	}
	if *deps != "" {
		a["depends_on"] = splitCSV(*deps)
	}
	raw, err := cl.Raw(ctx, "create_task", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ ID, Title, Status string }
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "created task %s: %q (%s)\n", r.ID, r.Title, r.Status)
	})
	return nil
}

func cmdTaskClaim(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task claim", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	agent := stringFlag(fs, "agent", "AGENTMESH_MEMBER", "", "claiming agent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "claim_task", map[string]any{"workspace": *ws, "agent": *agent})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Claimable bool `json:"claimable"`
			Task      struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"task"`
		}
		_ = json.Unmarshal(b, &r)
		if !r.Claimable {
			fmt.Fprintln(w, "(no claimable task)")
			return
		}
		fmt.Fprintf(w, "claimed task %s: %q\n", r.Task.ID, r.Task.Title)
	})
	return nil
}

func cmdTaskComplete(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task complete", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	agent := stringFlag(fs, "agent", "AGENTMESH_MEMBER", "", "assignee")
	id := fs.String("id", "", "task id")
	result := fs.String("result", "", "result/output text")
	fail := fs.Bool("fail", false, "mark the task failed instead of completed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "complete_task", map[string]any{
		"workspace": *ws, "id": *id, "agent": *agent, "result": *result, "done": !*fail,
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct{ ID, Status string }
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "task %s -> %s\n", r.ID, r.Status)
	})
	return nil
}

func cmdTaskGet(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task get", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	id := fs.String("id", "", "task id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "get_task", map[string]any{"workspace": *ws, "id": *id})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			ID, Title, Status, AssignedAgent string
		}
		// json field names differ; decode loosely.
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		r.ID, _ = m["id"].(string)
		r.Title, _ = m["title"].(string)
		r.Status, _ = m["status"].(string)
		r.AssignedAgent, _ = m["assigned_agent"].(string)
		fmt.Fprintf(w, "%s: %q [%s] assignee=%q\n", r.ID, r.Title, r.Status, r.AssignedAgent)
	})
	return nil
}

func cmdTaskList(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	status := fs.String("status", "", "comma-separated status filter: pending,claimed,completed,failed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a := map[string]any{"workspace": *ws}
	if *status != "" {
		a["statuses"] = splitCSV(*status)
	}
	raw, err := cl.Raw(ctx, "list_tasks", a)
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Count int `json:"count"`
			Tasks []struct {
				ID            string `json:"id"`
				Title         string `json:"title"`
				Status        string `json:"status"`
				AssignedAgent string `json:"assigned_agent"`
			} `json:"tasks"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "%d task(s):\n", r.Count)
		for _, t := range r.Tasks {
			assignee := ""
			if t.AssignedAgent != "" {
				assignee = " -> " + t.AssignedAgent
			}
			fmt.Fprintf(w, "  %s [%s]%s %q\n", t.ID, t.Status, assignee, t.Title)
		}
	})
	return nil
}

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
