package dashboard_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/bus"
	"github.com/indugapallignaneswara/agentmesh/internal/dashboard"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

func TestDashboard(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	svc := workspace.New(st, bus.NewNoop())
	srv := httptest.NewServer(dashboard.Handler(svc))
	t.Cleanup(srv.Close)

	// Seed: members, a task, a pending shared memory, an artifact.
	if _, err := svc.Join(ctx, "team", "alice", model.KindHuman, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Join(ctx, "team", "backend", model.KindAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTask(ctx, "team", "alice", "ship it", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MemoryWrite(ctx, "team", "backend", model.MemoryShared, "fact to review", "src"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ArtifactPut(ctx, "team", "alice", "notes", "hello", 0); err != nil {
		t.Fatal(err)
	}
	// The dashboard's usage panel reads the LEDGER via UsageStatsWindow, so
	// seed rows directly: measured bytes for backend plus a reported claim.
	now := time.Now().UTC()
	if err := st.AppendUsage(ctx, []model.UsageEvent{
		{TS: now, Workspace: "team", Member: "backend", Kind: model.KindAgent,
			Tool: "send_message", Direction: model.UsageIngress, Bytes: 4000},
		{TS: now, Workspace: "team", Member: "backend", Kind: model.KindAgent,
			Tool: "usage_report", Direction: model.UsageReported,
			ReportedPromptTokens: 1234, ReportedCompletionTokens: 56},
	}); err != nil {
		t.Fatal(err)
	}

	// The page is served.
	page, err := http.Get(srv.URL + "/ui")
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	body, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !strings.Contains(string(body), "AgentMesh") {
		t.Fatalf("/ui status=%d", page.StatusCode)
	}
	if ct := page.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}

	// The overview API reflects the seeded state.
	res, err := http.Get(srv.URL + "/ui/api?workspace=team&since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("/ui/api status=%d", res.StatusCode)
	}
	var ov struct {
		Presence    []struct{ Name string }    `json:"presence"`
		Tasks       []struct{ Title string }   `json:"tasks"`
		Events      []struct{ Type string }    `json:"events"`
		Cursor      int64                      `json:"cursor"`
		MemoryQueue []struct{ Content string } `json:"memory_queue"`
		Artifacts   []struct{ Name string }    `json:"artifacts"`
		Usage       struct {
			Members []struct {
				Member                   string `json:"member"`
				Kind                     string `json:"kind"`
				IngressBytes             int64  `json:"ingress_bytes"`
				EstTokens                int64  `json:"est_tokens"`
				ReportedPromptTokens     int64  `json:"reported_prompt_tokens"`
				ReportedCompletionTokens int64  `json:"reported_completion_tokens"`
			} `json:"members"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.Presence) != 2 || len(ov.Tasks) != 1 || len(ov.MemoryQueue) != 1 || len(ov.Artifacts) != 1 {
		t.Fatalf("overview = %+v", ov)
	}
	if ov.Cursor == 0 || len(ov.Events) == 0 {
		t.Fatalf("events empty: cursor=%d n=%d", ov.Cursor, len(ov.Events))
	}
	// The usage panel: measured bytes with a display-time estimate AND the
	// reported (client-claimed) tokens, side by side on the same member row.
	if len(ov.Usage.Members) != 1 {
		t.Fatalf("usage members = %+v, want exactly the seeded backend row", ov.Usage.Members)
	}
	um := ov.Usage.Members[0]
	if um.Member != "backend" || um.Kind != "agent" {
		t.Fatalf("usage row = %+v, want member backend kind agent", um)
	}
	if um.IngressBytes != 4000 || um.EstTokens <= 0 {
		t.Fatalf("usage row bytes/est = %d/%d, want 4000 measured bytes and a positive estimate", um.IngressBytes, um.EstTokens)
	}
	if um.ReportedPromptTokens != 1234 || um.ReportedCompletionTokens != 56 {
		t.Fatalf("usage row reported = %d/%d, want 1234/56", um.ReportedPromptTokens, um.ReportedCompletionTokens)
	}

	// Cursor paging: polling again from the cursor returns nothing new.
	res2, err := http.Get(srv.URL + "/ui/api?workspace=team&since=" + itoa(ov.Cursor))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	body2, _ := io.ReadAll(res2.Body)
	var ov2 struct {
		Events []struct{ Type string } `json:"events"`
	}
	if err := json.Unmarshal(body2, &ov2); err != nil {
		t.Fatal(err)
	}
	if len(ov2.Events) != 0 {
		t.Fatalf("expected no new events after cursor, got %d", len(ov2.Events))
	}
	// events must be [] (not null) when empty, so clients can iterate safely.
	if !strings.Contains(string(body2), `"events":[]`) {
		t.Fatalf("empty events should serialize as [], got: %s", body2)
	}

	// Invalid workspace -> 400.
	bad, err := http.Get(srv.URL + "/ui/api?workspace=bad%20name")
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad workspace status = %d, want 400", bad.StatusCode)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
