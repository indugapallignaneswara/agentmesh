package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// runAudit dispatches the `audit` subcommands. `audit query` talks to a
// RUNNING server's admin /audit endpoint (bearer-authenticated with the admin
// token), because the trail lives in the server's store.
func runAudit(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: agentiam audit query [--client ID] [--workspace W] [--type T] [--from RFC3339] [--to RFC3339] [--limit N] [--jsonl] [--endpoint URL] [--admin-token TOKEN]")
	}
	switch args[0] {
	case "query":
		return auditQuery(args[1:])
	default:
		return fmt.Errorf("unknown audit subcommand %q (want query)", args[0])
	}
}

func auditQuery(args []string) error {
	fs := flag.NewFlagSet("audit query", flag.ContinueOnError)
	endpoint := fs.String("endpoint", os.Getenv("AGENTIAM_ISSUER"),
		"base URL of the running agentiam server (default $AGENTIAM_ISSUER)")
	adminToken := fs.String("admin-token", os.Getenv("AGENTIAM_ADMIN_TOKEN"),
		"admin bearer token (default $AGENTIAM_ADMIN_TOKEN)")
	client := fs.String("client", "", "filter: client_id")
	workspace := fs.String("workspace", "", "filter: workspace")
	typ := fs.String("type", "", "filter: event type (e.g. token.issued)")
	from := fs.String("from", "", "filter: RFC3339 lower time bound (inclusive)")
	to := fs.String("to", "", "filter: RFC3339 upper time bound (exclusive)")
	limit := fs.Int("limit", 0, "max events (0 = server default)")
	jsonl := fs.Bool("jsonl", false, "print raw JSONL (the SIEM export) instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *endpoint == "" {
		return fmt.Errorf("--endpoint or AGENTIAM_ISSUER is required")
	}
	if *adminToken == "" {
		return fmt.Errorf("--admin-token or AGENTIAM_ADMIN_TOKEN is required")
	}

	q := url.Values{}
	if *client != "" {
		q.Set("client_id", *client)
	}
	if *workspace != "" {
		q.Set("workspace", *workspace)
	}
	if *typ != "" {
		q.Set("type", *typ)
	}
	if *from != "" {
		q.Set("from", *from)
	}
	if *to != "" {
		q.Set("to", *to)
	}
	if *limit > 0 {
		q.Set("limit", strconv.Itoa(*limit))
	}
	if *jsonl {
		q.Set("format", "jsonl")
	}
	u := strings.TrimRight(*endpoint, "/") + "/audit"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+*adminToken)

	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("GET /audit: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("audit query failed: status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}

	if *jsonl {
		// The SIEM export: pass the ndjson through untouched.
		_, err := io.Copy(os.Stdout, res.Body)
		return err
	}

	var body struct {
		Events []auditEventRow `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode /audit response: %w", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TS\tTYPE\tCLIENT\tSUBJECT\tJTI\tRESULT")
	for _, e := range body.Events {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.TS.UTC().Format(time.RFC3339), e.Type, e.ClientID, e.Subject, e.JTI, e.Result)
	}
	return tw.Flush()
}

// auditEventRow is the subset of iam.AuditEvent the table view prints. Decoded
// locally so the CLI stays a plain HTTP client of the /audit contract.
type auditEventRow struct {
	TS       time.Time `json:"ts"`
	Type     string    `json:"type"`
	ClientID string    `json:"client_id"`
	Subject  string    `json:"subject"`
	JTI      string    `json:"jti"`
	Result   string    `json:"result"`
}
