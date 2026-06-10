package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/config"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// runTokenCommand implements `agentmesh token create|list|revoke`. It talks to
// the database directly (same AGENTMESH_* env as the server), so credentials
// can be issued without a running server and with no bootstrap chicken-and-egg.
func runTokenCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agentmesh token <create|list|revoke> [flags]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Store != "postgres" {
		return fmt.Errorf("token management requires AGENTMESH_STORE=postgres")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "create":
		return tokenCreate(ctx, st, args[1:])
	case "list":
		return tokenList(ctx, st, args[1:])
	case "revoke":
		return tokenRevoke(ctx, st, args[1:])
	default:
		return fmt.Errorf("unknown token subcommand %q (want create|list|revoke)", args[0])
	}
}

func tokenCreate(ctx context.Context, st store.Store, args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	ws := fs.String("workspace", "", "workspace the token is bound to")
	member := fs.String("member", "", "member name the token authenticates as")
	kind := fs.String("kind", "agent", "member kind: human or agent")
	ttl := fs.Duration("ttl", 0, "optional lifetime (e.g. 720h); 0 = no expiry")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ws == "" || *member == "" {
		return fmt.Errorf("--workspace and --member are required")
	}
	k := model.Kind(*kind)
	if !k.Valid() {
		return fmt.Errorf("--kind must be 'human' or 'agent'")
	}

	secret, id, hash, err := auth.GenerateSecret()
	if err != nil {
		return err
	}
	t := model.AuthToken{
		ID: id, TokenHash: hash, Workspace: *ws, Member: *member, Kind: k,
		CreatedAt: time.Now().UTC(),
	}
	if *ttl > 0 {
		exp := t.CreatedAt.Add(*ttl)
		t.ExpiresAt = &exp
	}
	if _, err := st.CreateAuthToken(ctx, t); err != nil {
		return err
	}

	fmt.Printf("token id: %s\n", id)
	fmt.Printf("principal: %s/%s (%s)\n", *ws, *member, k)
	if t.ExpiresAt != nil {
		fmt.Printf("expires:  %s\n", t.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Printf("\n%s\n\n", secret)
	fmt.Fprintln(os.Stderr, "Store this secret now — it is shown once and only its hash is kept.")
	return nil
}

func tokenList(ctx context.Context, st store.Store, args []string) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	ws := fs.String("workspace", "", "workspace to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ws == "" {
		return fmt.Errorf("--workspace is required")
	}
	tokens, err := st.ListAuthTokens(ctx, *ws)
	if err != nil {
		return err
	}
	if len(tokens) == 0 {
		fmt.Println("(no tokens)")
		return nil
	}
	for _, t := range tokens {
		state := "active"
		if t.RevokedAt != nil {
			state = "revoked " + t.RevokedAt.Format(time.RFC3339)
		} else if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) {
			state = "expired " + t.ExpiresAt.Format(time.RFC3339)
		}
		fmt.Printf("%s  %s/%s (%s)  created %s  [%s]\n",
			t.ID, t.Workspace, t.Member, t.Kind, t.CreatedAt.Format(time.RFC3339), state)
	}
	return nil
}

func tokenRevoke(ctx context.Context, st store.Store, args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	id := fs.String("id", "", "token id to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return fmt.Errorf("--id is required")
	}
	if err := st.RevokeAuthToken(ctx, *id, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Printf("revoked %s\n", *id)
	return nil
}
