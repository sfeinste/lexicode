package mcp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/store"
)

// protocolVersion is the newest MCP revision this server implements; initialize echoes the
// client's requested version when it names one (per spec: the server answers with a version
// it supports — this subset behaves identically across revisions).
const protocolVersion = "2025-06-18"

// maxBody bounds one JSON-RPC message. Tool arguments are small (a wiki proposal body is the
// largest legitimate payload); a megabyte is an order of magnitude of headroom.
const maxBody = 1 << 20

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Handler returns the MCP endpoint handler. It resolves the run token from the URL path
// itself (never from mux path values), so the same handler serves the main mux pattern
// /mcp/{token} and the egress-proxy dispatch, where no pattern mux ran.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveMCP)
}

func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/mcp/")
	if token == "" || token == r.URL.Path || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	runID, ok := s.runForToken(token)
	if !ok {
		// Unknown and revoked are indistinguishable on purpose: 404, before any JSON-RPC
		// parsing, telling a guesser nothing.
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.serveRPC(w, r, runID)
	case http.MethodDelete:
		// Session teardown in streamable HTTP; this server is stateless per request.
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		// The optional server-initiated SSE stream; this server never speaks first.
		http.Error(w, "this MCP server does not offer a server-initiated stream", http.StatusMethodNotAllowed)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request, runID string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParse, Message: "parse error: " + err.Error()}})
		return
	}
	if req.Method == "" {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: codeInvalidRequest, Message: "missing method"}})
		return
	}

	// Notifications carry no id and expect no body: 202, accepted, ignored.
	if len(req.ID) == 0 || string(req.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = s.initializeResult(req.Params)
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolDescriptors()}
	case "tools/call":
		// A call the client asked for progress on, from a client that accepts an SSE
		// stream, is answered on one — notifications while it blocks, then the response
		// (see progress.go). Everything else answers with one JSON body.
		if token, ok := progressTokenOf(req.Params); ok && acceptsSSE(r) {
			s.callToolStreaming(w, r, runID, req, token)
			return
		}
		result, rpcErr := s.callTool(r, runID, req.Params)
		resp.Result, resp.Error = result, rpcErr
	default:
		resp.Error = &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
	writeRPC(w, resp)
}

func (s *Server) initializeResult(params json.RawMessage) map[string]any {
	version := protocolVersion
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "lexicode", "version": "1"},
	}
}

// callTool dispatches one tools/call. Tool-level failures are MCP tool results with
// isError=true (the agent reads them and adapts); only malformed JSON-RPC is an rpc error.
func (s *Server) callTool(r *http.Request, runID string, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}
	run, err := s.st.Runs().ByID(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError("this run no longer exists"), nil
		}
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}

	var result any
	var toolErr error
	switch p.Name {
	case "ask_human":
		result, toolErr = s.toolAskHuman(r.Context(), run, p.Arguments)
	case "set_step":
		result, toolErr = s.toolSetStep(r.Context(), run, p.Arguments)
	case "propose_wiki_page":
		result, toolErr = s.toolProposeWikiPage(r.Context(), run, p.Arguments)
	case "check_criterion":
		result, toolErr = s.toolCheckCriterion(r.Context(), run, p.Arguments)
	case "request_approval":
		result, toolErr = s.toolRequestApproval(r.Context(), run, p.Arguments)
	case "submit_review":
		result, toolErr = s.toolSubmitReview(r.Context(), run, p.Arguments)
	default:
		return toolError("unknown tool: " + p.Name), nil
	}
	if toolErr != nil {
		return toolError(toolErr.Error()), nil
	}
	return toolResult(result), nil
}

// toolResult wraps a tool's result object as MCP content: one JSON text block. Claude Code
// parses the text — for request_approval it must be exactly the {"behavior": …} document.
func toolResult(v any) map[string]any {
	text, err := json.Marshal(v)
	if err != nil {
		return toolError("marshal result: " + err.Error())
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(text)}},
		"isError": false,
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// toolDescriptors is the tools/list result: the five tools of contracts §3.3 plus
// submit_review (S39, the caller ports.ForgeProvider.SubmitReview was missing), schemas
// verbatim from the contract's shapes.
func toolDescriptors() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}

	return []map[string]any{
		{
			"name": "ask_human",
			"description": "Ask the delegating human one or more structured questions. " +
				"The run parks in `needs input` until they answer; the answer returns as this tool's result.",
			"inputSchema": obj(map[string]any{
				"questions": map[string]any{
					"type": "array",
					"items": obj(map[string]any{
						"question": str,
						"header":   map[string]any{"type": "string", "maxLength": 12},
						"options": map[string]any{
							"type":     "array",
							"minItems": 2, "maxItems": 4,
							"items": obj(map[string]any{
								"label": str, "description": str,
							}, "label"),
						},
						"multiSelect": boolean,
					}, "question", "header", "options"),
					"minItems": 1,
				},
			}, "questions"),
		},
		{
			"name": "set_step",
			"description": "Update the run's mutable current-step line, e.g. \"editing src/api/charge.ts\". " +
				"Fire and forget.",
			"inputSchema": obj(map[string]any{
				"step": str, "index": num, "total": num,
			}, "step"),
		},
		{
			"name": "propose_wiki_page",
			"description": "Propose a new wiki page, or an edit to an existing one (set edits_slug). " +
				"Proposals are never auto-written; a human reviews the diff.",
			"inputSchema": obj(map[string]any{
				"title": str, "slug": str, "parent": str, "body": str,
				"agent_scope": map[string]any{
					"type": "string",
					"enum": []string{"always", "auto", "paths", "manual", "never"},
				},
				"reason":     str,
				"edits_slug": str,
			}, "title", "body", "reason"),
		},
		{
			"name": "check_criterion",
			"description": "Mark one of this ticket's acceptance criteria met or unmet, with a note " +
				"saying how you verified it.",
			"inputSchema": obj(map[string]any{
				"criterion_id": str, "met": boolean, "note": str,
			}, "criterion_id", "met"),
		},
		{
			"name": "submit_review",
			"description": "Submit a review on a pull request with severity-tagged findings. " +
				"Severities: blocker, major, minor, nit. `event` defaults to request_changes " +
				"when any blocker or major finding is present and comment otherwise. " +
				"Agents cannot approve — approval is reserved for humans. " +
				"pr_number defaults to the pull request whose event started this run. " +
				"Tag findings honestly: the severities you report are what continues the " +
				"workflow, and GitHub may store a request_changes review as a plain comment " +
				"when the reviewer and the author share an account — the result then says " +
				"event=COMMENT with intended_event=REQUEST_CHANGES, and that is not a failure.",
			"inputSchema": obj(map[string]any{
				"pr_number": num,
				"event": map[string]any{
					"type": "string",
					"enum": []string{"comment", "request_changes"},
				},
				"summary": str,
				"findings": map[string]any{
					"type": "array",
					"items": obj(map[string]any{
						"severity": map[string]any{
							"type": "string",
							"enum": []string{"blocker", "major", "minor", "nit"},
						},
						"title": str, "detail": str, "file": str, "line": num,
					}, "severity", "title"),
				},
			}),
		},
		{
			"name": "request_approval",
			"description": "Request permission for a tool call. Backing tool for Claude Code's " +
				"--permission-prompt-tool; autonomy rules may answer without a human.",
			"inputSchema": obj(map[string]any{
				"tool_name": str,
				"input":     map[string]any{"type": "object"},
			}, "tool_name", "input"),
		},
	}
}

// runProject loads the run's project, for topic naming and membership checks.
func (s *Server) runProject(r *http.Request, runID string) (domain.Run, domain.Project, error) {
	run, err := s.st.Runs().ByID(r.Context(), runID)
	if err != nil {
		return domain.Run{}, domain.Project{}, err
	}
	p, err := s.st.Projects().ByID(r.Context(), run.ProjectID)
	return run, p, err
}
