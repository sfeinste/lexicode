// Module health composition (LEXI-9).
//
// The kernel gives a module exactly one state and one reason string (kernel.SetModuleState,
// added by S14). The github module, though, can have several independent things wrong at the
// same time: the rate-limit budget can be spent while the token separately cannot read check
// suites. Reporting them through the same one-slot API from two places races — whichever
// recovers first would clear the other's reason and leave the module falsely `ready`.
//
// moduleHealth is that one slot, owned in one place. Every cause registers under a stable key
// and clears under the same key; the module is `ready` only when no cause is outstanding, and
// the reason is every outstanding cause's own sentence, in a stable order.
package github

import (
	"sort"
	"strings"
	"sync"

	"github.com/spruce/lexicode/internal/kernel"
)

// rateLimitKey is the health key the S14 transport's exhausted/recovered pair registers under.
// The poller's keys are project-and-resource scoped and cannot collide with it.
const rateLimitKey = "\x00rate-limit"

type moduleHealth struct {
	// report is the sink. It is read at call time, not captured, because Module.Init
	// replaces the forge's health func after New has already built this.
	report func(kernel.ModuleState, string)

	mu     sync.Mutex
	causes map[string]string // key → the sentence shown to the user
	state  kernel.ModuleState
	reason string
}

func newModuleHealth(report func(kernel.ModuleState, string)) *moduleHealth {
	return &moduleHealth{report: report, causes: map[string]string{}, state: kernel.StateReady}
}

// degrade records one cause. It reports true the first time a given key degrades (and on a
// changed reason for the same key), which is what callers use to log once instead of per tick.
func (h *moduleHealth) degrade(key, reason string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, had := h.causes[key]; had && prev == reason {
		return false
	}
	h.causes[key] = reason
	h.applyLocked()
	return true
}

// recover clears one cause. It reports true when that key was actually outstanding, so a
// caller can log a recovery exactly once.
func (h *moduleHealth) recover(key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, had := h.causes[key]; !had {
		return false
	}
	delete(h.causes, key)
	h.applyLocked()
	return true
}

// applyLocked composes the state and the reason from the outstanding causes and forwards the
// result when it changed. Keys are sorted so a workspace with two denied resources does not
// flap its reason string between ticks. The report runs under h.mu on purpose: it serialises
// transitions, and the kernel never calls back into this type.
func (h *moduleHealth) applyLocked() {
	state, reason := kernel.StateReady, ""
	if len(h.causes) > 0 {
		keys := make([]string, 0, len(h.causes))
		for k := range h.causes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, h.causes[k])
		}
		state, reason = kernel.StateDegraded, strings.Join(parts, " ")
	}
	if h.state == state && h.reason == reason {
		return
	}
	h.state, h.reason = state, reason
	if h.report != nil {
		h.report(state, reason)
	}
}
