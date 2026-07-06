package workspace

import (
	"context"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// Memory event type names recorded in the episodic log.
const (
	EventMemoryWritten   = "memory_written"
	EventMemorySubmitted = "memory_submitted"
	EventMemoryApproved  = "memory_approved"
	EventMemoryRejected  = "memory_rejected"
)

const (
	maxMemoryContent    = 16 * 1024
	maxMemorySource     = 512
	maxMemoryQuery      = 512
	defaultMemoryLimit  = 10
	maxMemoryLimit      = 100
	maxMemoryReviewNote = 1024
)

// MemoryWrite stores a memory item for author. Private items are approved
// immediately and visible only to the author. Shared items enter the review
// queue as pending — they are not retrievable by anyone until a human reviewer
// approves them. This is the write path's anti-poisoning quarantine: agents
// can propose shared knowledge but never publish it directly.
func (s *Service) MemoryWrite(ctx context.Context, workspace, author string, scope model.MemoryScope, content, source string) (model.Memory, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Memory{}, err
	}
	if err := validName("author", author); err != nil {
		return model.Memory{}, err
	}
	if scope != model.MemoryPrivate && scope != model.MemoryShared {
		return model.Memory{}, fmt.Errorf("%w: scope must be %q or %q", ErrInvalidInput, model.MemoryPrivate, model.MemoryShared)
	}
	if content == "" || len(content) > maxMemoryContent {
		return model.Memory{}, fmt.Errorf("%w: content must be 1-%d bytes", ErrInvalidInput, maxMemoryContent)
	}
	if len(source) > maxMemorySource {
		return model.Memory{}, fmt.Errorf("%w: source must be at most %d bytes", ErrInvalidInput, maxMemorySource)
	}
	if err := auth.CheckActor(ctx, workspace, author); err != nil {
		return model.Memory{}, err
	}
	if err := s.requireOpenRoom(ctx, workspace); err != nil {
		return model.Memory{}, err
	}
	if err := s.requireMember(ctx, workspace, author); err != nil {
		return model.Memory{}, err
	}

	now := s.now()
	m := model.Memory{
		ID:        s.newID(),
		Workspace: workspace,
		Scope:     scope,
		Content:   content,
		Source:    source,
		CreatedBy: author,
		CreatedAt: now,
		UpdatedAt: now,
	}
	eventType := EventMemoryWritten
	if scope == model.MemoryPrivate {
		m.Owner = author
		m.Status = model.MemoryApproved
	} else {
		m.Status = model.MemoryPending
		eventType = EventMemorySubmitted
	}

	created, err := s.store.CreateMemory(ctx, m)
	if err != nil {
		return model.Memory{}, err
	}
	s.touch(ctx, workspace, author)
	s.appendEvent(ctx, workspace, author, eventType, map[string]any{
		"memory_id": created.ID, "scope": scope,
	})
	return created, nil
}

// MemorySearch returns the memories visible to requester matching the query:
// the requester's own private items plus approved shared items. Pending and
// rejected shared items never appear, and one member can never see another's
// private memories.
func (s *Service) MemorySearch(ctx context.Context, workspace, requester, query string, limit int) ([]model.Memory, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := validName("requester", requester); err != nil {
		return nil, err
	}
	if query == "" || len(query) > maxMemoryQuery {
		return nil, fmt.Errorf("%w: query must be 1-%d bytes", ErrInvalidInput, maxMemoryQuery)
	}
	switch {
	case limit <= 0:
		limit = defaultMemoryLimit
	case limit > maxMemoryLimit:
		limit = maxMemoryLimit
	}
	// requester decides private-memory visibility, so identity must be proven.
	if err := auth.CheckActor(ctx, workspace, requester); err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, workspace, requester); err != nil {
		return nil, err
	}
	out, err := s.store.SearchMemories(ctx, workspace, requester, query, limit)
	if err != nil {
		return nil, err
	}
	s.touch(ctx, workspace, requester)
	return out, nil
}

// MemoryQueue returns the pending shared items awaiting review. Only human
// members may inspect the queue.
func (s *Service) MemoryQueue(ctx context.Context, workspace, reviewer string) ([]model.Memory, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := auth.CheckActor(ctx, workspace, reviewer); err != nil {
		return nil, err
	}
	if err := s.requireHuman(ctx, workspace, reviewer); err != nil {
		return nil, err
	}
	return s.store.ListPendingShared(ctx, workspace)
}

// MemoryQueuePeek returns the pending shared submissions for the web
// dashboard. Unlike MemoryQueue it takes no reviewer principal: the dashboard
// is an inherently human surface, and Phases 0-3 ship without authentication —
// the MCP-tool guard (MemoryQueue) exists to keep *agents* from pulling
// quarantined content into their context, which it still does. When Phase 4
// adds auth, this endpoint gets gated with the rest of the UI.
func (s *Service) MemoryQueuePeek(ctx context.Context, workspace string) ([]model.Memory, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := auth.CheckWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	return s.store.ListPendingShared(ctx, workspace)
}

// MemoryReview approves or rejects a pending shared item. The reviewer must be
// a human member and must not be the item's author — an agent (or the author)
// can never push its own submission into shared memory.
func (s *Service) MemoryReview(ctx context.Context, workspace, reviewer, id string, approve bool, note string) (model.Memory, error) {
	if err := validName("workspace", workspace); err != nil {
		return model.Memory{}, err
	}
	if id == "" {
		return model.Memory{}, fmt.Errorf("%w: memory id is required", ErrInvalidInput)
	}
	if len(note) > maxMemoryReviewNote {
		return model.Memory{}, fmt.Errorf("%w: note must be at most %d bytes", ErrInvalidInput, maxMemoryReviewNote)
	}
	if err := auth.CheckActor(ctx, workspace, reviewer); err != nil {
		return model.Memory{}, err
	}
	if err := s.requireHuman(ctx, workspace, reviewer); err != nil {
		return model.Memory{}, err
	}
	existing, err := s.store.GetMemory(ctx, workspace, id)
	if err != nil {
		return model.Memory{}, err
	}
	if existing.CreatedBy == reviewer {
		return model.Memory{}, fmt.Errorf("%w: cannot review your own submission", ErrInvalidInput)
	}

	reviewed, err := s.store.ReviewMemory(ctx, workspace, id, reviewer, approve, note, s.now())
	if err != nil {
		return model.Memory{}, err
	}
	s.touch(ctx, workspace, reviewer)
	eventType := EventMemoryApproved
	if !approve {
		eventType = EventMemoryRejected
	}
	s.appendEvent(ctx, workspace, reviewer, eventType, map[string]any{
		"memory_id": id, "author": existing.CreatedBy,
	})
	return reviewed, nil
}

// requireHuman verifies the named member exists and is human. Review authority
// is restricted to humans in this phase; a configurable "librarian agent"
// allowlist is a later extension.
func (s *Service) requireHuman(ctx context.Context, workspace, name string) error {
	if err := validName("reviewer", name); err != nil {
		return err
	}
	m, err := s.store.GetMember(ctx, workspace, name)
	if err != nil {
		if err == store.ErrNotFound {
			return fmt.Errorf("member %q: %w", name, store.ErrNotFound)
		}
		return err
	}
	if m.Kind != model.KindHuman {
		return fmt.Errorf("%w: %q is not a human member (memory review requires a human reviewer)", ErrInvalidInput, name)
	}
	return nil
}
