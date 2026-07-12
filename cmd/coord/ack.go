package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

// cmdAck finalises messages read with `coord inbox --ack`.
func cmdAck(ctx context.Context, cl *client.Client, out *output, args []string) error {
	fs := flag.NewFlagSet("ack", flag.ContinueOnError)
	ws := stringFlag(fs, "workspace", "AGENTMESH_WORKSPACE", "", "workspace id")
	member := stringFlag(fs, "member", "AGENTMESH_MEMBER", "", "acknowledging member")
	ids := fs.String("ids", "", "comma-separated message ids from an ack-mode read")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := cl.Raw(ctx, "ack_messages", map[string]any{
		"workspace": *ws, "member": *member, "ids": splitCSV(*ids),
	})
	if err != nil {
		return err
	}
	out.emit(raw, func(w io.Writer, b []byte) {
		var r struct {
			Acked int `json:"acked"`
		}
		_ = json.Unmarshal(b, &r)
		fmt.Fprintf(w, "acknowledged %d message(s)\n", r.Acked)
	})
	return nil
}
