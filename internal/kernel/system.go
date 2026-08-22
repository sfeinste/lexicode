package kernel

import (
	"net/http"

	"github.com/spruce/lexicode/internal/kernel/audit"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
)

// SystemModulesPath is the route this file serves. It is a constant so that the frontend contract
// and the tests refer to the same string.
const SystemModulesPath = "GET /api/v1/system/modules"

// StreamPath is the SSE endpoint (contracts §5.1).
const StreamPath = "GET /api/v1/stream"

// AuditPath is the audit query endpoint (contracts §5).
const AuditPath = "GET /api/v1/audit"

// RevokeSessionsPath is the S37 owner action "revoke a user's sessions". It lives in the
// kernel because it composes two kernel pieces the services never hold together: the auth
// service (which owns sessions but cannot import the audit writer without a cycle) and the
// audit writer (every mutation is audited).
const RevokeSessionsPath = "DELETE /api/v1/users/{id}/sessions"

// modulesResponse is the body of GET /api/v1/system/modules. It is an object rather than a bare
// array so that later stories can add fields (a boot timestamp, the store's migration version)
// without changing the shape a client already parses.
type modulesResponse struct {
	Modules []ModuleStatus `json:"modules"`
}

// registerSystemRoutes registers the routes the kernel owns itself: the module list, the SSE
// stream and the audit query. All sit behind RequireAuth when the kernel was built with an auth
// service (cmd/lexicode always does; only kernel tests do not) — module names, live events and
// the audit trail are nobody else's business; the audit log is further owner-only.
func (k *Kernel) registerSystemRoutes() {
	var modules http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := modulesResponse{Modules: k.Modules()}
		if body.Modules == nil {
			// Encode an empty list, never null: a client that renders "no modules" should not
			// have to distinguish the two.
			body.Modules = []ModuleStatus{}
		}
		httpx.WriteJSON(w, http.StatusOK, body)
	})
	if k.auth != nil {
		modules = k.auth.RequireAuth(modules)
	}
	k.mux.Handle(SystemModulesPath, modules)

	if k.sse != nil && k.auth != nil {
		k.mux.Handle(StreamPath, k.auth.RequireAuth(k.sse))
	}
	if k.audit != nil && k.auth != nil {
		k.mux.Handle(AuditPath, k.auth.RequireAuth(auth.RequireOwner(k.audit.Handler())))
		k.mux.Handle(RevokeSessionsPath,
			k.auth.RequireAuth(auth.RequireOwner(http.HandlerFunc(k.handleRevokeSessions))))
	}
}

// handleRevokeSessions is DELETE /api/v1/users/{id}/sessions (owner only): every session of
// the named user dies now, and the revocation is audited at workspace level with the acting
// owner as the actor.
func (k *Kernel) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	n, err := k.auth.RevokeSessions(r.Context(), userID)
	if err != nil {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"No such user", "No user matches this id.")
		return
	}
	if err := k.audit.Write(r.Context(), "user.sessions.revoke",
		audit.Target{Kind: "user", ID: userID}, nil, map[string]int64{"revoked": n}); err != nil {
		httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
			"Internal error", "The sessions were revoked but the audit write failed.")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}
