package workspace

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/store"
)

// invitePrefix marks invite codes ("ami_" + 256 bits URL-safe base64) so they
// are visually distinct from auth tokens ("amt_") and greppable in leaks.
const invitePrefix = "ami_"

const maxInviteUses = 1000

// generateInviteCode returns a new invite credential: the plaintext code to
// hand out ONCE, its public ID, and the hash to store (never the code).
func generateInviteCode() (code, id, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", fmt.Errorf("entropy: %w", err)
	}
	code = invitePrefix + base64.RawURLEncoding.EncodeToString(raw)
	// The ID is a short, non-secret handle derived from the hash (not the
	// code) so logs and list output never contain credential material.
	hash = auth.HashSecret(code)
	id = "inv_" + hash[:12]
	return code, id, hash, nil
}

// RoomInviteCreate mints an invite code for the room. Only a human moderator
// may mint; the plaintext code is returned exactly once and never stored.
// role is the role granted on join (member or moderator — never owner);
// maxUses caps redemptions (default 1); ttl <= 0 means no expiry.
func (s *Service) RoomInviteCreate(ctx context.Context, ws, actor string, kind model.Kind, role model.Role, maxUses int, ttl time.Duration) (string, model.Invite, error) {
	if err := validName("workspace", ws); err != nil {
		return "", model.Invite{}, err
	}
	if _, err := s.requireModerator(ctx, ws, actor); err != nil {
		return "", model.Invite{}, err
	}
	if !kind.Valid() {
		return "", model.Invite{}, fmt.Errorf("%w: kind must be %q or %q", ErrInvalidInput, model.KindHuman, model.KindAgent)
	}
	if role == "" {
		role = model.RoleMember
	}
	// Invited members can never arrive as owner.
	if role != model.RoleMember && role != model.RoleModerator {
		return "", model.Invite{}, fmt.Errorf("%w: role must be %q or %q", ErrInvalidInput, model.RoleModerator, model.RoleMember)
	}
	if maxUses <= 0 {
		maxUses = 1
	}
	if maxUses > maxInviteUses {
		return "", model.Invite{}, fmt.Errorf("%w: max_uses must be at most %d", ErrInvalidInput, maxInviteUses)
	}
	code, id, hash, err := generateInviteCode()
	if err != nil {
		return "", model.Invite{}, err
	}
	now := s.now()
	inv := model.Invite{
		ID: id, CodeHash: hash, Workspace: ws, Kind: kind, Role: role,
		MaxUses: maxUses, CreatedBy: actor, CreatedAt: now,
	}
	if ttl > 0 {
		exp := now.Add(ttl)
		inv.ExpiresAt = &exp
	}
	stored, err := s.store.CreateInvite(ctx, inv)
	if err != nil {
		return "", model.Invite{}, err
	}
	// The event payload deliberately excludes the code (and hash): the
	// episodic log is broadly readable and must never leak credentials.
	s.appendEvent(ctx, ws, actor, EventInviteCreated, map[string]any{
		"invite_id": stored.ID, "kind": stored.Kind, "role": stored.Role, "max_uses": stored.MaxUses,
	})
	return code, stored, nil
}

// RoomInviteRevoke soft-revokes one of the room's invites by ID (moderator
// only). An invite belonging to a different room is ErrNotFound — IDs are
// global, but authority is per-room.
func (s *Service) RoomInviteRevoke(ctx context.Context, ws, actor, id string) error {
	if err := validName("workspace", ws); err != nil {
		return err
	}
	if _, err := s.requireModerator(ctx, ws, actor); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("%w: invite id is required", ErrInvalidInput)
	}
	// Verify the invite belongs to this room before revoking, so a moderator
	// of room A cannot revoke room B's invites. (Listing is fine at invite
	// scale; a GetInviteByID store method can replace this if it ever isn't.)
	invs, err := s.store.ListInvites(ctx, ws)
	if err != nil {
		return err
	}
	found := false
	for _, inv := range invs {
		if inv.ID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("invite %q: %w", id, store.ErrNotFound)
	}
	if err := s.store.RevokeInvite(ctx, id, s.now()); err != nil {
		return err // ErrNotFound if already revoked
	}
	s.appendEvent(ctx, ws, actor, EventInviteRevoked, map[string]any{"invite_id": id})
	return nil
}

// RoomInvites lists the room's invites, newest first (moderator only). The
// records carry usage/revocation state but never the codes.
func (s *Service) RoomInvites(ctx context.Context, ws, actor string) ([]model.Invite, error) {
	if err := validName("workspace", ws); err != nil {
		return nil, err
	}
	if _, err := s.requireModerator(ctx, ws, actor); err != nil {
		return nil, err
	}
	return s.store.ListInvites(ctx, ws)
}

// RoomSetPolicy sets the room's join and broadcast policies (moderator only).
func (s *Service) RoomSetPolicy(ctx context.Context, ws, actor string, jp model.JoinPolicy, bp model.BroadcastPolicy) (model.Workspace, error) {
	if err := validName("workspace", ws); err != nil {
		return model.Workspace{}, err
	}
	if _, err := s.requireModerator(ctx, ws, actor); err != nil {
		return model.Workspace{}, err
	}
	if !jp.Valid() {
		return model.Workspace{}, fmt.Errorf("%w: join_policy must be %q or %q", ErrInvalidInput, model.JoinOpen, model.JoinInvite)
	}
	if !bp.Valid() {
		return model.Workspace{}, fmt.Errorf("%w: who_may_broadcast must be %q or %q", ErrInvalidInput, model.BroadcastAnyone, model.BroadcastModerators)
	}
	w, err := s.store.SetWorkspacePolicy(ctx, ws, jp, bp, s.now())
	if err != nil {
		return model.Workspace{}, err
	}
	s.appendEvent(ctx, ws, actor, EventRoomPolicyChanged, map[string]any{
		"join_policy": jp, "who_may_broadcast": bp,
	})
	return w, nil
}

// JoinWithInvite joins a room using an invite code, working even when the
// room's join policy is invite-only.
//
// Ordering note: the room-open and ban checks run BEFORE the code is redeemed
// (mirroring Join's own checks) so a join that was going to fail anyway does
// not burn a use. The invite is also fetched and validated (right room, right
// kind) before redemption for the same reason. Between RedeemInvite and the
// final join a failure can still strand a burned use (acceptable v1: the
// window is one Upsert, and a moderator can mint another code).
func (s *Service) JoinWithInvite(ctx context.Context, ws, name string, kind model.Kind, card json.RawMessage, code string) (model.Member, error) {
	if err := validName("workspace", ws); err != nil {
		return model.Member{}, err
	}
	if err := validName("name", name); err != nil {
		return model.Member{}, err
	}
	if !kind.Valid() {
		return model.Member{}, fmt.Errorf("%w: kind must be %q or %q", ErrInvalidInput, model.KindHuman, model.KindAgent)
	}
	if code == "" {
		return model.Member{}, fmt.Errorf("%w: invite code is required", ErrInvalidInput)
	}
	// Pre-flight the checks joinInternal would fail on, before burning a use.
	if err := auth.CheckActor(ctx, ws, name); err != nil {
		return model.Member{}, err
	}
	if err := auth.CheckKind(ctx, kind); err != nil {
		return model.Member{}, err
	}
	if len(card) > maxAgentCardSize {
		return model.Member{}, fmt.Errorf("%w: agent_card must be at most %d bytes", ErrInvalidInput, maxAgentCardSize)
	}
	if len(card) > 0 && !json.Valid(card) {
		return model.Member{}, fmt.Errorf("%w: agent_card is not valid JSON", ErrInvalidInput)
	}
	if err := s.requireOpenRoom(ctx, ws); err != nil {
		return model.Member{}, err
	}
	if _, err := s.store.GetBan(ctx, ws, name); err == nil {
		return model.Member{}, fmt.Errorf("%w: %q", store.ErrBanned, name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Member{}, err
	}

	hash := auth.HashSecret(code)
	inv, err := s.store.GetInviteByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return model.Member{}, fmt.Errorf("invite code: %w", store.ErrNotFound)
		}
		return model.Member{}, err
	}
	// Validate the invite's scope before redeeming so a mismatch costs nothing.
	if inv.Workspace != ws {
		return model.Member{}, fmt.Errorf("%w: invite is for a different room", ErrInvalidInput)
	}
	if inv.Kind != kind {
		return model.Member{}, fmt.Errorf("%w: invite admits kind %q, not %q", ErrInvalidInput, inv.Kind, kind)
	}

	// Atomic redemption: revoked/expired/exhausted all fail here with
	// ErrInviteSpent, and concurrent redeemers can never overshoot max_uses.
	inv, err = s.store.RedeemInvite(ctx, hash, s.now())
	if err != nil {
		return model.Member{}, err
	}

	m, err := s.joinInternal(ctx, ws, name, kind, card, true)
	if err != nil {
		return model.Member{}, err
	}
	// A moderator-granting invite promotes the member on arrival — but never
	// demotes an owner (the creator re-joining with an invite stays owner).
	if inv.Role == model.RoleModerator && m.Role != model.RoleOwner && m.Role != model.RoleModerator {
		promoted, err := s.store.SetMemberRole(ctx, ws, name, inv.Role)
		if err != nil {
			return model.Member{}, err
		}
		m = promoted
	}
	return m, nil
}
