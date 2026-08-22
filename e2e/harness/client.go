package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// Client is the driver's view of the product: the same HTTP API the dashboard uses, with the
// same session cookie. Nothing in the drivers reaches around it except the two documented
// places (the repos row the API does not expose yet, and the fixture git repository).
type Client struct {
	Base string
	http *http.Client
	// LastRun is the most recent run body Do read, for failure messages.
	LastRun map[string]any
}

// NewClient builds a client with a cookie jar, so Setup/Login sessions carry.
func NewClient(base string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{Base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}, nil
}

// Do performs one API call and returns the decoded body, failing if the status is not one of
// wantStatus.
func (c *Client) Do(method, path string, body any, wantStatus ...int) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.Base+path, rd) //nolint:noctx // fixture driver
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	okStatus := false
	for _, want := range wantStatus {
		if resp.StatusCode == want {
			okStatus = true
		}
	}
	if !okStatus {
		return nil, fmt.Errorf("%s %s = %d, want %v: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	out := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("%s %s: not JSON: %s", method, path, raw)
		}
	}
	return out, nil
}

// RunState returns a run's state, or "" if it cannot be read.
func (c *Client) RunState(id string) string {
	row, err := c.Do("GET", "/api/v1/runs/"+id, nil, 200)
	if err != nil {
		return ""
	}
	r, _ := row["run"].(map[string]any)
	c.LastRun = r
	st, _ := r["state"].(string)
	return st
}

// Run returns a run's body.
func (c *Client) Run(id string) (map[string]any, error) {
	row, err := c.Do("GET", "/api/v1/runs/"+id, nil, 200)
	if err != nil {
		return nil, err
	}
	r, _ := row["run"].(map[string]any)
	return r, nil
}

// ActivityRow is one run activity, as the driver asserts on it.
type ActivityRow struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	ToolName string `json:"tool_name"`
	OK       *bool  `json:"ok"`
}

// Activities returns a run's activity rows.
func (c *Client) Activities(runID string) ([]ActivityRow, error) {
	body, err := c.Do("GET", "/api/v1/runs/"+runID+"/activities", nil, 200)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body["activities"])
	if err != nil {
		return nil, err
	}
	var out []ActivityRow
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PendingElicitation returns the id of a run's one pending elicitation.
func (c *Client) PendingElicitation(runID string) (string, error) {
	row, err := c.Do("GET", "/api/v1/runs/"+runID, nil, 200)
	if err != nil {
		return "", err
	}
	els, _ := row["elicitations"].([]any)
	for _, e := range els {
		el := e.(map[string]any)
		if el["state"] == "pending" {
			return el["id"].(string), nil
		}
	}
	return "", fmt.Errorf("no pending elicitation on run %s: %s", runID, Compact(els))
}

// TicketColumnCategory resolves a ticket's column category — never its name (brief D2).
func (c *Client) TicketColumnCategory(projectKey, ticketID string) (string, error) {
	tk, err := c.Do("GET", "/api/v1/tickets/"+ticketID, nil, 200)
	if err != nil {
		return "", err
	}
	columnID, _ := tk["column_id"].(string)
	cols, err := c.Do("GET", "/api/v1/projects/"+projectKey+"/columns", nil, 200)
	if err != nil {
		return "", err
	}
	for _, colAny := range cols["columns"].([]any) {
		col := colAny.(map[string]any)
		if col["id"] == columnID {
			cat, _ := col["category"].(string)
			return cat, nil
		}
	}
	return "", fmt.Errorf("ticket column %q is not in the column list", columnID)
}

// Outputs returns a run's output kinds.
func (c *Client) Outputs(runID string) ([]map[string]any, error) {
	row, err := c.Do("GET", "/api/v1/runs/"+runID, nil, 200)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, o := range row["outputs"].([]any) {
		out = append(out, o.(map[string]any))
	}
	return out, nil
}
