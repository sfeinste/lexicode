package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/auth"
	"github.com/spruce/lexicode/internal/kernel/httpx"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// Routes registers the S15 endpoints. All are project-member guarded (see the package comment
// for why member, not owner):
//
//	POST   /api/v1/projects/{key}/repo                 connect / reconnect
//	GET    /api/v1/projects/{key}/repo                 connection status
//	DELETE /api/v1/projects/{key}/repo                 disconnect (imported data stays)
//	PATCH  /api/v1/projects/{key}/repo/network         network policy + allowlist (S18)
//	POST   /api/v1/projects/{key}/bootstrap/preview    the one-payload checklist; writes nothing
//	POST   /api/v1/projects/{key}/bootstrap/apply      creates exactly the checked subset
func (s *Service) Routes(mux httpx.Registrar, a *auth.Service) {
	member := func(h http.HandlerFunc) http.Handler {
		return a.RequireAuth(a.RequireProjectMember(h))
	}
	mux.Handle("POST /api/v1/projects/{key}/repo", member(s.handleConnect))
	mux.Handle("GET /api/v1/projects/{key}/repo", member(s.handleStatus))
	mux.Handle("DELETE /api/v1/projects/{key}/repo", member(s.handleDisconnect))
	mux.Handle("PATCH /api/v1/projects/{key}/repo/network", member(s.handleUpdateNetwork))
	mux.Handle("POST /api/v1/projects/{key}/bootstrap/preview", member(s.handlePreview))
	mux.Handle("POST /api/v1/projects/{key}/bootstrap/apply", member(s.handleApply))
}

// repoBody is how the connection renders: everything the About card and the settings pane
// need. The token never appears — only the fact that one is stored.
type repoBody struct {
	Provider      string  `json:"provider"`
	Owner         string  `json:"owner"`
	Name          string  `json:"name"`
	DefaultBranch *string `json:"default_branch"`
	HeadSHA       *string `json:"head_sha"`
	HeadMessage   *string `json:"head_message"`
	ConnectedAt   *string `json:"connected_at"`
	LastSyncedAt  *string `json:"last_synced_at"`
	HasToken      bool    `json:"has_token"`
	// The S18 network settings: the nullable override (null = inherit), the allowlist, and
	// the live workspace default so the UI's InheritedField line never recomputes inheritance.
	NetworkPolicy          *string  `json:"network_policy"`
	NetworkAllowlist       []string `json:"network_allowlist"`
	WorkspaceNetworkPolicy string   `json:"workspace_network_policy"`
}

// repoBody assembles the wire shape, fetching the workspace default the inheritance line
// needs. The single workspace_settings row always exists (migration 0001 inserts it).
func (s *Service) repoBody(ctx context.Context, rp domain.Repo) (repoBody, error) {
	ws, err := s.st.Workspace().Get(ctx)
	if err != nil {
		return repoBody{}, err
	}
	allow := rp.NetworkAllowlist
	if allow == nil {
		allow = []string{}
	}
	return repoBody{
		Provider: rp.Provider, Owner: rp.Owner, Name: rp.Name,
		DefaultBranch: rp.DefaultBranch, HeadSHA: rp.HeadSHA, HeadMessage: rp.HeadMessage,
		ConnectedAt: rp.ConnectedAt, LastSyncedAt: rp.LastSyncedAt,
		HasToken:      rp.TokenSecretID != nil,
		NetworkPolicy: rp.NetworkPolicy, NetworkAllowlist: allow,
		WorkspaceNetworkPolicy: ws.DefaultNetworkPolicy,
	}, nil
}

type connectBody struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

func (s *Service) handleConnect(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[connectBody](w, r)
	if !ok {
		return
	}
	rp, err := s.Connect(r.Context(), r.PathValue("key"), ConnectInput(body))
	if err != nil {
		s.writeError(w, err)
		return
	}
	rb, err := s.repoBody(r.Context(), rp)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rb)
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	rp, err := s.Status(r.Context(), r.PathValue("key"))
	if errors.Is(err, store.ErrNotFound) {
		// No repo yet is a normal state the connect gate renders, not a 404 to handle.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"connected": false})
		return
	}
	if err != nil {
		s.writeError(w, err)
		return
	}
	rb, err := s.repoBody(r.Context(), rp)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"connected": true, "repo": rb,
	})
}

func (s *Service) handleUpdateNetwork(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[NetworkSettingsInput](w, r)
	if !ok {
		return
	}
	rp, err := s.UpdateNetworkSettings(r.Context(), r.PathValue("key"), body)
	if err != nil {
		s.writeError(w, err)
		return
	}
	rb, err := s.repoBody(r.Context(), rp)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rb)
}

func (s *Service) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.Disconnect(r.Context(), r.PathValue("key")); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	pv, err := s.BuildPreview(r.Context(), r.PathValue("key"))
	if err != nil {
		s.writeError(w, err)
		return
	}
	if pv.Issues == nil {
		pv.Issues = []IssueCandidate{}
	}
	if pv.Docs == nil {
		pv.Docs = []DocCandidate{}
	}
	if pv.Triggers == nil {
		pv.Triggers = []TriggerCandidate{}
	}
	if pv.Agents == nil {
		pv.Agents = []AgentCandidate{}
	}
	httpx.WriteJSON(w, http.StatusOK, pv)
}

func (s *Service) handleApply(w http.ResponseWriter, r *http.Request) {
	body, ok := httpx.DecodeJSON[ApplyInput](w, r)
	if !ok {
		return
	}
	res, err := s.Apply(r.Context(), r.PathValue("key"), body)
	if err != nil {
		s.writeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// writeError maps service errors to problems: field errors → 400, missing rows → 404,
// everything else → a logged 500.
func (s *Service) writeError(w http.ResponseWriter, err error) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		httpx.WriteValidation(w, ve.Fields)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteProblem(w, http.StatusNotFound, httpx.TypeNotFound,
			"Not found", "No repository is connected to this project.")
		return
	}
	s.logger.Error("bootstrap: request failed", slog.String("error", err.Error()))
	httpx.WriteProblem(w, http.StatusInternalServerError, httpx.TypeInternal,
		"Internal error", "Something went wrong on the server. The error has been logged.")
}
