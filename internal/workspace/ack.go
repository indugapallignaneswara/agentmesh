package workspace

import (
	"context"
	"fmt"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// ReadInboxAck is the at-least-once inbox read: messages are leased for the
// ack-visibility window instead of consumed. Unless the caller acknowledges
// them with AckMessages they are redelivered after the window — so a crashed
// reader (or a lost response) never loses a message. Same guards as ReadInbox:
// only the member itself may lease its inbox.
func (s *Service) ReadInboxAck(ctx context.Context, workspace, member string) ([]model.Message, error) {
	if err := validName("workspace", workspace); err != nil {
		return nil, err
	}
	if err := validName("member", member); err != nil {
		return nil, err
	}
	if err := auth.CheckActor(ctx, workspace, member); err != nil {
		return nil, err
	}
	if err := s.requireMember(ctx, workspace, member); err != nil {
		return nil, err
	}
	msgs, err := s.store.ReadInboxLeased(ctx, workspace, member, s.now(), s.ackVisibility)
	if err != nil {
		return nil, err
	}
	s.annotateSenderKinds(ctx, workspace, msgs)
	s.touch(ctx, workspace, member)
	return msgs, nil
}

// AckMessages finalises an at-least-once read: the given message ids are
// marked delivered and will never redeliver. Foreign/unknown ids are ignored;
// the returned count is how many were actually acknowledged.
func (s *Service) AckMessages(ctx context.Context, workspace, member string, ids []string) (int, error) {
	if err := validName("workspace", workspace); err != nil {
		return 0, err
	}
	if err := validName("member", member); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("%w: ids are required", ErrInvalidInput)
	}
	if len(ids) > maxAckIDs {
		return 0, fmt.Errorf("%w: at most %d ids per ack", ErrInvalidInput, maxAckIDs)
	}
	for _, id := range ids {
		if id == "" {
			return 0, fmt.Errorf("%w: empty message id", ErrInvalidInput)
		}
	}
	if err := auth.CheckActor(ctx, workspace, member); err != nil {
		return 0, err
	}
	if err := s.requireMember(ctx, workspace, member); err != nil {
		return 0, err
	}
	n, err := s.store.AckInbox(ctx, workspace, member, ids, s.now())
	if err != nil {
		return 0, err
	}
	s.touch(ctx, workspace, member)
	return n, nil
}
