package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/indugapallignaneswara/agentmesh/internal/auth"
	"github.com/indugapallignaneswara/agentmesh/internal/metrics"
	"github.com/indugapallignaneswara/agentmesh/internal/model"
	"github.com/indugapallignaneswara/agentmesh/internal/usage"
	"github.com/indugapallignaneswara/agentmesh/internal/workspace"
)

// NewServerWithObservability is NewServerWithMetrics plus usage metering. When
// rec is non-nil every tools/call is metered: ingress = the raw argument bytes
// the caller sent in, egress = the serialized result returned to the caller
// (reader-pays — see docs/token-metering.md §2). Metering is measure-only and
// best-effort: Record never blocks, so tool latency is unaffected.
func NewServerWithObservability(svc *workspace.Service, version string, reg *metrics.Registry, rec *usage.Recorder) *mcp.Server {
	s := NewServerWithMetrics(svc, version, reg)
	if rec != nil {
		s.AddReceivingMiddleware(meterUsage(rec, reg))
	}
	return s
}

// HandlerWithObservability is HandlerWithMetrics plus usage metering.
func HandlerWithObservability(svc *workspace.Service, version string, reg *metrics.Registry, rec *usage.Recorder) http.Handler {
	srv := NewServerWithObservability(svc, version, reg, rec)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)
}

// meterUsage records two usage events per tool call: the caller's ingress
// (bytes they wrote into the mesh — their completion tokens at their vendor)
// and the caller's egress (bytes the mesh returned — future prompt tokens in
// their context window). We meter the wire: the serialized CallToolResult is
// what actually enters the caller's context, including the ok() helper's
// text+structured double encoding (documented in docs/token-metering.md §9,
// deliberately not hidden).
func meterUsage(rec *usage.Recorder, reg *metrics.Registry) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || method != "tools/call" {
				return next(ctx, method, req)
			}
			res, err := next(ctx, method, req)

			tool := "unknown"
			var raw json.RawMessage
			if call.Params != nil {
				tool = call.Params.Name
				raw = call.Params.Arguments
			}
			ws, member, kind, authed := attributeCall(ctx, raw)
			now := time.Now().UTC()

			ingress := int64(len(raw))
			rec.Record(model.UsageEvent{
				TS: now, Workspace: ws, Member: member, Kind: kind,
				Tool: tool, Direction: model.UsageIngress,
				Bytes: ingress, Authenticated: authed,
			})
			if reg != nil {
				reg.ObserveUsage(ws, tool, string(model.UsageIngress), ingress)
			}

			// Egress only for a delivered result; a protocol error returned
			// nothing into the caller's context. Tool-level errors (IsError)
			// ARE metered — their text still lands in the caller's context.
			if ctr, isCall := res.(*mcp.CallToolResult); isCall && ctr != nil && err == nil {
				if out, mErr := json.Marshal(ctr); mErr == nil {
					egress := int64(len(out))
					rec.Record(model.UsageEvent{
						TS: now, Workspace: ws, Member: member, Kind: kind,
						Tool: tool, Direction: model.UsageEgress,
						Bytes: egress, Authenticated: authed,
					})
					if reg != nil {
						reg.ObserveUsage(ws, tool, string(model.UsageEgress), egress)
					}
				}
			}
			return res, err
		}
	}
}

// attributeCall resolves who a call belongs to. With auth on, the verified
// Principal is authoritative (authenticated=true). With auth off there is no
// principal, so we sniff the tool's own arguments — a claimed identity,
// recorded as such (authenticated=false), matching the trusted-LAN posture.
func attributeCall(ctx context.Context, raw json.RawMessage) (ws, member string, kind model.Kind, authed bool) {
	if p, ok := auth.FromContext(ctx); ok {
		return p.Workspace, p.Member, p.Kind, true
	}
	if len(raw) == 0 {
		return "", "", "", false
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) != nil {
		return "", "", "", false
	}
	str := func(key string) string {
		if v, ok := args[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
		return ""
	}
	ws = str("workspace")
	// The actor field differs per tool family; first hit wins.
	for _, key := range []string{"from", "member", "name", "actor", "source", "creator", "reviewer", "agent"} {
		if member = str(key); member != "" {
			break
		}
	}
	if k := model.Kind(str("kind")); k.Valid() {
		kind = k
	}
	return ws, member, kind, false
}
