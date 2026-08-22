package bootstrap

// Network policy settings (story S18, D-10): the two repos columns the egress proxy enforces —
// network_policy (nullable; null inherits the workspace default_network_policy) and
// network_allowlist. Edited from the project settings Repository pane; the proxy reads the
// resolved values per run when the scheduler (S22) registers it.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/audit"
)

// validPolicies are the three D-10 stances.
var validPolicies = []string{"none", "allowlist", "open"}

// OptString is a tri-state JSON field for PATCH bodies, the string twin of the projects
// service's OptInt: absent (leave unchanged), null (revert to inherit), or a value.
type OptString struct {
	Set   bool
	Null  bool
	Value string
}

// UnmarshalJSON records that the field appeared, and whether it was null.
func (o *OptString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

// NetworkSettingsInput is the PATCH body for the repo's network settings. A nil allowlist
// leaves the stored list unchanged.
type NetworkSettingsInput struct {
	NetworkPolicy    OptString `json:"network_policy"`
	NetworkAllowlist *[]string `json:"network_allowlist"`
}

// UpdateNetworkSettings validates and persists the repo's network policy override and
// allowlist. Both writes are audited with the honest before/after values — a network policy
// change is exactly the kind of thing an audit trail exists for.
func (s *Service) UpdateNetworkSettings(ctx context.Context, projectKey string, in NetworkSettingsInput) (domain.Repo, error) {
	p, err := s.st.Projects().ByKey(ctx, projectKey)
	if err != nil {
		return domain.Repo{}, err
	}
	rp, err := s.st.Repos().ByProject(ctx, p.ID)
	if err != nil {
		return domain.Repo{}, err
	}
	before := rp

	if in.NetworkPolicy.Set {
		switch {
		case in.NetworkPolicy.Null:
			rp.NetworkPolicy = nil
		case slices.Contains(validPolicies, in.NetworkPolicy.Value):
			v := in.NetworkPolicy.Value
			rp.NetworkPolicy = &v
		default:
			return domain.Repo{}, fieldErr("network_policy",
				fmt.Sprintf("%q is not a network policy; use none, allowlist or open.", in.NetworkPolicy.Value))
		}
	}

	if in.NetworkAllowlist != nil {
		cleaned, err := cleanAllowlist(*in.NetworkAllowlist)
		if err != nil {
			return domain.Repo{}, err
		}
		rp.NetworkAllowlist = cleaned
	}

	rp.UpdatedAt = s.now()
	if err := s.st.Repos().Update(ctx, &rp); err != nil {
		return domain.Repo{}, err
	}

	if err := s.audit.Write(ctx, "repo.network.update",
		audit.Target{Kind: "repo", ID: p.ID, ProjectID: p.ID},
		networkAudit(before), networkAudit(rp)); err != nil {
		return domain.Repo{}, err
	}
	s.emit(ctx, "repo.network_updated", p, map[string]any{
		"network_policy":    rp.NetworkPolicy,
		"network_allowlist": rp.NetworkAllowlist,
	})
	return rp, nil
}

// networkAudit is the audit shape of the network columns only — the fields this mutation can
// touch, nothing else.
func networkAudit(rp domain.Repo) map[string]any {
	return map[string]any{
		"network_policy":    rp.NetworkPolicy,
		"network_allowlist": rp.NetworkAllowlist,
	}
}

// cleanAllowlist normalizes and validates the domain list: entries are trimmed and
// lowercased, blanks dropped, duplicates collapsed. An entry is a bare domain, optionally
// with a `*.` wildcard prefix (matches the domain and every subdomain) — never a URL, a
// path, or a lone `*`.
func cleanAllowlist(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, entry := range raw {
		d := strings.ToLower(strings.TrimSpace(entry))
		if d == "" {
			continue
		}
		if err := validateDomain(d); err != nil {
			return nil, fieldErr("network_allowlist", err.Error())
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out, nil
}

func validateDomain(d string) error {
	bare := strings.TrimPrefix(d, "*.")
	if bare == "" || bare == "*" || strings.Contains(bare, "*") {
		return fmt.Errorf("%q is not a valid entry; a wildcard is spelled *.example.com", d)
	}
	if strings.ContainsAny(bare, "/:@?#& \t") {
		return fmt.Errorf("%q is not a domain; list bare domains such as registry.npmjs.org, without scheme or path", d)
	}
	if !strings.Contains(bare, ".") {
		return fmt.Errorf("%q is not a domain; list fully qualified domains such as registry.npmjs.org", d)
	}
	return nil
}
