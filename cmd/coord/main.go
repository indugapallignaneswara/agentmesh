// Command coord is a thin CLI over the AgentMesh MCP server. It is the fallback
// integration path for agents with weak/no MCP support (e.g. Aider) and the
// engine behind session hooks and slash commands: every subcommand maps to one
// coordination tool call.
//
// Usage:
//
//	coord [--endpoint URL] [--json] <command> [flags]
//
// Commands: join, presence, send, inbox, ack, broadcast, publish, subscribe,
// task, memory, artifact, room, invite, mod, leave, history (run "coord -h" for
// the full list). Several flags fall back to environment variables so hooks can
// stay terse:
//
//	AGENTMESH_ENDPOINT   default endpoint (else http://localhost:8080/mcp)
//	AGENTMESH_WORKSPACE  default --workspace
//	AGENTMESH_MEMBER     default member identity (--name/--member/--from/--source)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/client"
)

const defaultEndpoint = "http://localhost:8080/mcp"

// callTimeout bounds a single CLI invocation end to end.
const callTimeout = 30 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "coord: "+err.Error())
		os.Exit(1)
	}
}

// run dispatches to a subcommand. Global flags (--endpoint, --json) may appear
// before the subcommand name.
func run(args []string) error {
	endpoint := envOr("AGENTMESH_ENDPOINT", defaultEndpoint)
	token := os.Getenv("AGENTMESH_TOKEN")
	jsonOut := false

	// Consume leading global flags.
	for len(args) > 0 {
		switch args[0] {
		case "--json", "-json":
			jsonOut = true
			args = args[1:]
		case "--endpoint", "-endpoint":
			if len(args) < 2 {
				return fmt.Errorf("--endpoint requires a value")
			}
			endpoint = args[1]
			args = args[2:]
		case "--token", "-token":
			if len(args) < 2 {
				return fmt.Errorf("--token requires a value")
			}
			token = args[1]
			args = args[2:]
		case "-h", "--help", "help":
			usage()
			return nil
		default:
			goto dispatch
		}
	}

dispatch:
	if len(args) == 0 {
		usage()
		return fmt.Errorf("a command is required")
	}

	cmd, rest := args[0], args[1:]
	var opts []client.Option
	if token != "" {
		opts = append(opts, client.WithToken(token))
	}
	cl := client.New(endpoint, opts...)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()

	out := &output{json: jsonOut, w: os.Stdout}

	switch cmd {
	case "join":
		return cmdJoin(ctx, cl, out, rest)
	case "presence":
		return cmdPresence(ctx, cl, out, rest)
	case "send":
		return cmdSend(ctx, cl, out, rest)
	case "inbox":
		return cmdInbox(ctx, cl, out, rest)
	case "ack":
		return cmdAck(ctx, cl, out, rest)
	case "broadcast":
		return cmdBroadcast(ctx, cl, out, rest)
	case "publish":
		return cmdPublish(ctx, cl, out, rest)
	case "subscribe":
		return cmdSubscribe(ctx, cl, out, rest)
	case "task":
		return cmdTask(ctx, cl, out, rest)
	case "memory":
		return cmdMemory(ctx, cl, out, rest)
	case "artifact":
		return cmdArtifact(ctx, cl, out, rest)
	case "room":
		return cmdRoom(ctx, cl, out, rest)
	case "invite":
		return cmdInvite(ctx, cl, out, rest)
	case "mod":
		return cmdModerate(ctx, cl, out, rest)
	case "leave":
		return cmdLeave(ctx, cl, out, rest)
	case "history":
		return cmdHistory(ctx, cl, out, rest)
	case "usage":
		return cmdUsage(ctx, cl, out, rest)
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// stringFlag registers a string flag whose default comes from an env var.
func stringFlag(fs *flag.FlagSet, name, env, def, usage string) *string {
	return fs.String(name, envOr(env, def), usage)
}

func usage() {
	fmt.Fprint(os.Stderr, `coord — AgentMesh coordination CLI

Usage: coord [--endpoint URL] [--json] <command> [flags]

Commands:
  join       Join or refresh workspace membership
  presence   List members active now
  send       Send a direct message to one member (any-to-any)
  inbox      Read your undelivered messages (--ack leases them: at-least-once)
  ack        Finalise messages from an ack-mode read (--ids id1,id2)
  broadcast  Send a message to all other members
  publish    Append an event to the observation log
  subscribe  Read events after a cursor
  task       Shared task board: task create|claim|complete|get|list
  memory     Shared memory: memory write|search|queue|approve|reject
  artifact   Co-edited artifacts: artifact get|put|list
  room       Room lifecycle: room create|close|reopen|list
             Room policy: room policy --join open|invite --broadcast anyone|moderators
  invite     Room invites (human moderators): invite create|list|revoke
  mod        Moderation (human moderators): mod kick|ban|unban|bans|role
  leave      Leave the room (self-service departure)
  history    Review the room's message log (human-only, non-consuming)
  usage      Per-member coordination usage for the room (bytes in/out, est tokens)

Global flags:
  --endpoint URL   MCP endpoint (env AGENTMESH_ENDPOINT, default `+defaultEndpoint+`)
  --token SECRET   Bearer token when the server requires auth (env AGENTMESH_TOKEN)
  --json           Print the raw JSON tool result

Run "coord <command> -h" for command-specific flags.
`)
}
