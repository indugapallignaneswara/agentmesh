package mcpserver

import (
	"encoding/json"

	"github.com/indugapallignaneswara/agentmesh/internal/model"
)

// The MCP SDK derives each tool's *output* schema from the Go type a handler
// returns. Domain types carry JSON blobs as json.RawMessage ([]byte) so they can
// be scanned straight from Postgres jsonb, but a []byte field generates a
// "null or array" schema that then rejects a real JSON object at output
// validation time. These DTOs mirror the domain types with blob fields typed as
// `any`, which generates a permissive schema and round-trips the decoded JSON
// value faithfully. Conversion happens at the transport boundary only.

type memberDTO struct {
	Workspace string     `json:"workspace"`
	Name      string     `json:"name"`
	Kind      model.Kind `json:"kind"`
	AgentCard any        `json:"agent_card,omitempty"`
	JoinedAt  string     `json:"joined_at"`
	LastSeen  string     `json:"last_seen"`
}

type eventDTO struct {
	Seq       int64  `json:"seq"`
	Workspace string `json:"workspace"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Payload   any    `json:"payload,omitempty"`
	CreatedAt string `json:"created_at"`
}

// rawToAny decodes a stored JSON blob into a generic value for output. An empty
// blob (the field was unset) becomes nil so it is omitted from the response.
func rawToAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// The store only ever holds JSON it validated on the way in, so this is
		// not expected; surface the raw text rather than dropping data.
		return string(raw)
	}
	return v
}

const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func toMemberDTO(m model.Member) memberDTO {
	return memberDTO{
		Workspace: m.Workspace,
		Name:      m.Name,
		Kind:      m.Kind,
		AgentCard: rawToAny(m.AgentCard),
		JoinedAt:  m.JoinedAt.UTC().Format(rfc3339Nano),
		LastSeen:  m.LastSeen.UTC().Format(rfc3339Nano),
	}
}

func toMemberDTOs(ms []model.Member) []memberDTO {
	out := make([]memberDTO, len(ms))
	for i, m := range ms {
		out[i] = toMemberDTO(m)
	}
	return out
}

func toEventDTO(e model.Event) eventDTO {
	return eventDTO{
		Seq:       e.Seq,
		Workspace: e.Workspace,
		Source:    e.Source,
		Type:      e.Type,
		Payload:   rawToAny(e.Payload),
		CreatedAt: e.CreatedAt.UTC().Format(rfc3339Nano),
	}
}

func toEventDTOs(es []model.Event) []eventDTO {
	out := make([]eventDTO, len(es))
	for i, e := range es {
		out[i] = toEventDTO(e)
	}
	return out
}
