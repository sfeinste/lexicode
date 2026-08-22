package runs

import (
	"encoding/json"
	"fmt"

	"github.com/spruce/lexicode/internal/domain"
)

// claudeSettings builds the container's `.claude/settings.json` from the agent's permissions.
// This is enforcement, not guidance (brief D7): the checkboxes in agent settings compile to
// Claude Code allow/deny rules the CLI itself refuses on, never to prompt text.
//
// The mapping table (documented here because this file is its single source of truth):
//
//	agents.permissions     Claude Code tools                       false means
//	------------------     ------------------------------------    ------------------------
//	read_files             Read, Grep, Glob                        deny all three
//	edit_files             Edit, Write, NotebookEdit               deny all three
//	run_commands           Bash                                    deny Bash entirely
//	push_branches          — (no tool of its own)                  deny Bash(git push:*)
//
// push_branches also exists as a hard refusal in the forge adapter; the deny rule here closes
// the `git push` path that runs through Bash with the repo token embedded in the origin URL.
// open_prs, comment_prs, submit_reviews and create_wiki_pages are enforced server-side — in
// the forge adapter (S14) and the Lexicode MCP server (S21) — because those actions never
// happen through a container-local tool; settings.json has nothing to say about them.
//
// Deny wins over allow in Claude Code, so the deny list is written even for grants that are
// off — an explicit deny survives a permissive user setting layered underneath.
func claudeSettings(p domain.AgentPermissions) ([]byte, error) {
	allow, deny := []string{}, []string{} // empty arrays, never JSON null
	grant := func(granted bool, tools ...string) {
		if granted {
			allow = append(allow, tools...)
		} else {
			deny = append(deny, tools...)
		}
	}
	grant(p.ReadFiles, "Read", "Grep", "Glob")
	grant(p.EditFiles, "Edit", "Write", "NotebookEdit")
	grant(p.RunCommands, "Bash")
	if !p.PushBranches {
		deny = append(deny, "Bash(git push:*)")
	}

	type permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	doc := struct {
		Permissions permissions `json:"permissions"`
	}{permissions{Allow: allow, Deny: deny}}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling .claude/settings.json: %w", err)
	}
	return append(out, '\n'), nil
}
