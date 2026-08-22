package runs

import (
	"strings"
	"sync"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

// redactedPlaceholder replaces every registered secret in anything user-visible.
const redactedPlaceholder = "[REDACTED]"

// minSecretLength guards against registering a string so short that redaction would shred
// ordinary text.
const minSecretLength = 4

// Redactor holds the secret values of one run — the OAuth token, the repo token, the clone
// URL, every injected secret — and scrubs them out of user-visible strings. It is the same
// approach as the github module's log redactor, applied to the run pipeline: everything that
// becomes an activity, a provisioning log line or an API-visible error passes through Clean,
// so a container that echoes its environment still cannot leak a credential into the
// transcript (S19 acceptance).
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

// Add registers secret values. Empty and very short strings are ignored; duplicates collapse.
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
next:
	for _, v := range values {
		if len(v) < minSecretLength {
			continue
		}
		for _, s := range r.secrets {
			if s == v {
				continue next
			}
		}
		r.secrets = append(r.secrets, v)
	}
}

// Clean replaces every registered secret in s with the placeholder.
func (r *Redactor) Clean(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, redactedPlaceholder)
	}
	return s
}

// redactingSink wraps a ProvisionSink so that every step detail and log line is cleaned
// before the inner sink — the activity writer — sees it.
type redactingSink struct {
	inner ports.ProvisionSink
	r     *Redactor
}

// NewRedactingSink wraps sink so nothing a registered secret appears in reaches it. The
// scheduler (S22) wraps its activity-writing sink with the run's Redactor before handing it
// to Sandbox.Prepare.
func NewRedactingSink(sink ports.ProvisionSink, r *Redactor) ports.ProvisionSink {
	return &redactingSink{inner: sink, r: r}
}

// Step implements ports.ProvisionSink.
func (s *redactingSink) Step(name string, state ports.StepState, detail string) {
	s.inner.Step(name, state, s.r.Clean(detail))
}

// Log implements ports.ProvisionSink.
func (s *redactingSink) Log(line string) {
	s.inner.Log(s.r.Clean(line))
}
