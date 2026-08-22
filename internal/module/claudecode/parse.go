package claudecode

import (
	"encoding/json"
	"strings"
)

// The stream-json wire shapes. This file is, with format.go, the only code in the system that
// knows Claude Code's output format (contracts §3.2). Everything is decoded permissively —
// unknown fields are ignored, missing fields zero — because a malformed or novel line must
// degrade to a level-2 system activity, never kill the run.

// streamLine is the envelope of one NDJSON line on stdout.
type streamLine struct {
	Type      string `json:"type"`    // "system" | "assistant" | "user" | "result"
	Subtype   string `json:"subtype"` // system: "init"; result: "success" | "error_*"
	SessionID string `json:"session_id"`

	// system/init fields.
	CWD   string   `json:"cwd"`
	Tools []string `json:"tools"`
	Model string   `json:"model"`

	// assistant / user wrapper.
	Message *apiMessage `json:"message"`

	// result fields.
	IsError       bool            `json:"is_error"`
	Result        string          `json:"result"`
	NumTurns      int64           `json:"num_turns"`
	DurationMS    int64           `json:"duration_ms"`
	DurationAPIMS int64           `json:"duration_api_ms"`
	TotalCostUSD  float64         `json:"total_cost_usd"`
	Usage         json.RawMessage `json:"usage"`
}

// apiMessage is the Anthropic API message embedded in assistant and user lines.
type apiMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Content []contentBlock  `json:"content"`
	Usage   json.RawMessage `json:"usage"`
}

// contentBlock is one block of an API message's content array.
type contentBlock struct {
	Type string `json:"type"` // "text" | "tool_use" | "tool_result" | "thinking"

	// text / thinking.
	Text     string `json:"text"`
	Thinking string `json:"thinking"`

	// tool_use.
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// tool_result.
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// apiUsage is the usage object of one API call (and of the final result line).
type apiUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
}

// resultText flattens a tool_result content field, which the CLI emits either as a plain JSON
// string or as an array of {type:"text",text:"..."} blocks.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := make([]string, 0, len(blocks))
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	// Neither shape: keep the raw JSON as text so nothing is silently dropped.
	return string(raw)
}
