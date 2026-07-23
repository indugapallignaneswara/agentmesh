package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// runToken dispatches the `token` subcommands. Unlike the `client` admin
// commands (which talk to the store directly), `token revoke` talks to a
// RUNNING server's RFC 7009 /revoke endpoint, because revocation must land in
// the same denylist the server publishes on /revocations.
func runToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agentiam token revoke --client ID --secret SECRET --token JWT [--endpoint URL]")
	}
	switch args[0] {
	case "revoke":
		return tokenRevoke(args[1:])
	default:
		return fmt.Errorf("unknown token subcommand %q (want revoke)", args[0])
	}
}

func tokenRevoke(args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	endpoint := fs.String("endpoint", os.Getenv("AGENTIAM_ISSUER"),
		"base URL of the running agentiam server (default $AGENTIAM_ISSUER)")
	client := fs.String("client", "", "client_id that obtained the token")
	secret := fs.String("secret", "", "the client's secret")
	token := fs.String("token", "", "the access token (JWT) to revoke")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" {
		return fmt.Errorf("--endpoint or AGENTIAM_ISSUER is required")
	}
	if *client == "" || *secret == "" || *token == "" {
		return fmt.Errorf("--client, --secret and --token are required")
	}

	form := url.Values{"token": {*token}}
	req, err := http.NewRequest(http.MethodPost,
		strings.TrimRight(*endpoint, "/")+"/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(*client, *secret)

	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("POST /revoke: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("revoke failed: status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	fmt.Println("revoked")
	return nil
}
