package store

import (
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// effectiveStatus is the status a task should report given the current time. A
// claimed task whose lease has expired is reported as pending, because it is
// once again claimable — keeping list reads consistent with claim eligibility.
// Both store implementations use this so they agree.
func effectiveStatus(t model.Task, now time.Time) model.TaskStatus {
	if t.Status == model.TaskClaimed && t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.After(now) {
		return model.TaskPending
	}
	return t.Status
}

// filterByStatus returns only tasks whose status is in want. An empty want
// returns all tasks unchanged.
func filterByStatus(tasks []model.Task, want []model.TaskStatus) []model.Task {
	if len(want) == 0 {
		return tasks
	}
	set := make(map[model.TaskStatus]bool, len(want))
	for _, s := range want {
		set[s] = true
	}
	out := tasks[:0:0]
	for _, t := range tasks {
		if set[t.Status] {
			out = append(out, t)
		}
	}
	return out
}
