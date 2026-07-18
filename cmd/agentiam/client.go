package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/iam"
)

// runClient dispatches the `client` admin subcommands. They talk to the store
// directly (same AGENTIAM_DATABASE_URL as the server) so credentials can be
// issued without a running server.
func runClient(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agentiam client <register|list|disable> [flags]")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, closeStore, err := openStore(ctx, log)
	if err != nil {
		return err
	}
	defer closeStore()

	switch args[0] {
	case "register":
		return clientRegister(ctx, store, args[1:])
	case "list":
		return clientList(ctx, store, args[1:])
	case "disable":
		return clientDisable(ctx, store, args[1:])
	default:
		return fmt.Errorf("unknown client subcommand %q (want register|list|disable)", args[0])
	}
}

func clientRegister(ctx context.Context, store iam.Store, args []string) error {
	fs := flag.NewFlagSet("client register", flag.ContinueOnError)
	ws := fs.String("workspace", "", "workspace (room) the client is bound to")
	member := fs.String("member", "", "member name a token authenticates as (the token `sub`)")
	kind := fs.String("kind", "agent", "member kind: agent or human")
	scopes := fs.String("scopes", "", "comma/space separated allowed scopes")
	ttl := fs.Duration("ttl", 0, "access-token lifetime (0 = server default)")
	budget := fs.Int64("budget-daily-bytes", 0, "daily coordination-byte cap stamped into every token (0 = none)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ws == "" || *member == "" {
		return fmt.Errorf("--workspace and --member are required")
	}

	clientID, secret, err := iam.RegisterClient(ctx, store, iam.Client{
		Workspace:        *ws,
		Subject:          *member,
		Kind:             *kind,
		AllowedScopes:    iam.ParseScopeList(*scopes),
		TokenTTL:         *ttl,
		BudgetDailyBytes: *budget,
	})
	if err != nil {
		return err
	}

	fmt.Printf("client_id:     %s\n", clientID)
	fmt.Printf("principal:     %s/%s (%s)\n", *ws, *member, *kind)
	if *scopes != "" {
		fmt.Printf("allowed scopes: %s\n", *scopes)
	}
	if *budget > 0 {
		fmt.Printf("budget:        %d bytes/day (carried in every token)\n", *budget)
	}
	fmt.Printf("\nclient_secret: %s\n\n", secret)
	fmt.Fprintln(os.Stderr, "Store this secret now — it is shown once and only its hash is kept.")
	return nil
}

func clientList(ctx context.Context, store iam.Store, args []string) error {
	fs := flag.NewFlagSet("client list", flag.ContinueOnError)
	ws := fs.String("workspace", "", "filter by workspace (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	clients, err := store.ListClients(ctx, *ws)
	if err != nil {
		return err
	}
	if len(clients) == 0 {
		fmt.Println("(no clients)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CLIENT_ID\tWORKSPACE\tMEMBER\tKIND\tSTATE\tCREATED")
	for _, c := range clients {
		state := "active"
		if c.Disabled {
			state = "disabled"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ClientID, c.Workspace, c.Subject, c.Kind, state, c.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

func clientDisable(ctx context.Context, store iam.Store, args []string) error {
	fs := flag.NewFlagSet("client disable", flag.ContinueOnError)
	id := fs.String("id", "", "client_id to disable")
	enable := fs.Bool("enable", false, "re-enable instead of disable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if err := store.SetClientDisabled(ctx, *id, !*enable); err != nil {
		return err
	}
	if *enable {
		fmt.Printf("enabled %s\n", *id)
	} else {
		fmt.Printf("disabled %s\n", *id)
	}
	return nil
}
