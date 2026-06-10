// Package dashboard serves the built-in human web UI: live presence, the
// event stream, the task board, the memory review queue and artifacts — all
// rendered by a single embedded page polling one JSON endpoint. It deliberately
// adds no runtime dependencies; Centrifugo (or any websocket layer) remains the
// documented scale-up path if polling ever becomes a bottleneck.
package dashboard

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

//go:embed ui.html
var uiHTML []byte

// overview is the JSON payload the page polls.
type overview struct {
	Workspace   string           `json:"workspace"`
	Presence    []model.Member   `json:"presence"`
	Tasks       []model.Task     `json:"tasks"`
	Events      []model.Event    `json:"events"`
	Cursor      int64            `json:"cursor"`
	MemoryQueue []model.Memory   `json:"memory_queue"`
	Artifacts   []model.Artifact `json:"artifacts"`
}

// Handler returns the dashboard handler. Mount it at /ui (it serves both /ui
// and /ui/api).
func Handler(svc *workspace.Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(uiHTML)
	})

	mux.HandleFunc("GET /ui/api", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ws := r.URL.Query().Get("workspace")
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)

		ov := overview{Workspace: ws}
		var err error
		if ov.Presence, err = svc.Presence(ctx, ws); err != nil {
			httpErr(w, err)
			return
		}
		// The remaining sections share the validated workspace; any failure is
		// surfaced the same way.
		if ov.Tasks, err = svc.ListTasks(ctx, ws, nil); err != nil {
			httpErr(w, err)
			return
		}
		if ov.Events, ov.Cursor, err = svc.Subscribe(ctx, ws, "", since, 100); err != nil {
			httpErr(w, err)
			return
		}
		if ov.MemoryQueue, err = svc.MemoryQueuePeek(ctx, ws); err != nil {
			httpErr(w, err)
			return
		}
		if ov.Artifacts, err = svc.ArtifactList(ctx, ws); err != nil {
			httpErr(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ov)
	})

	return mux
}

func httpErr(w http.ResponseWriter, err error) {
	if errors.Is(err, workspace.ErrInvalidInput) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
