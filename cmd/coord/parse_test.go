package main

import (
	"flag"
	"testing"
)

// TestParsePositionalInterspersed is the regression guard for the QA finding
// that `coord memory search "query" --limit 1` silently ignored --limit
// (Go's flag.Parse stops at the first positional). parsePositional must honour
// flags before, after, and interspersed with positional args.
func TestParsePositionalInterspersed(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantText  string
		wantLimit int
	}{
		{"flag before positional", []string{"--limit", "1", "hello", "world"}, "hello world", 1},
		{"flag after positional", []string{"hello", "world", "--limit", "1"}, "hello world", 1},
		{"flag interspersed", []string{"hello", "--limit", "1", "world"}, "hello world", 1},
		{"no flag, default", []string{"just", "text"}, "just text", 10},
		{"only flag", []string{"--limit", "5"}, "", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("t", flag.ContinueOnError)
			limit := fs.Int("limit", 10, "")
			text, err := parsePositional(fs, tc.args)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
			if *limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", *limit, tc.wantLimit)
			}
		})
	}
}
