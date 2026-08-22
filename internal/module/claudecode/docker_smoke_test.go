//go:build docker

package claudecode_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/module/claudecode"
	docker "github.com/spruce/lexicode/internal/module/docker"
)

// exitMarker is written by the fake agent only on the path where it read stdin to EOF. Its
// presence after the session is proof the process ended the way the real CLI does, rather
// than by exiting on its own or being killed.
const exitMarker = "/workspace/.lexicode/exited-on-stdin-eof"

// fakeClaude is a stand-in agent for the docker smoke test: it consumes the prompt message
// from stdin the way the real CLI would, then emits a canned stream-json session. Launching
// the real claude CLI would need credentials; this exercises everything else — the §3.1
// launch path (pidfile wrapper, exact argv, prompt on stdin) and the full docker Exec →
// stream → parser → activities pipeline against a real container.
//
// Crucially it does NOT exit after its result. Under `--input-format stream-json` the real
// CLI treats a result as the end of a turn and goes back to reading stdin, exiting only at
// EOF; a fixture that exits by itself hides an adapter that never closes stdin. The blocking
// read below is what makes this fixture honest — and it hangs forever unless the adapter
// closes stdin when the last turn ends.
const fakeClaude = `#!/bin/sh
head -n 1 >/dev/null
cat <<'EOF'
{"type":"system","subtype":"init","cwd":"/workspace","session_id":"smoke","tools":["Bash"],"model":"fake"}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"checking the workspace"}],"usage":{"input_tokens":5,"output_tokens":9}}}
{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls /workspace"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"README.md"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"result":"smoke complete","total_cost_usd":0.005,"usage":{"input_tokens":5,"output_tokens":9}}
EOF
cat >/dev/null
: > ` + exitMarker + `
exit 0
`

// TestDockerSmokeScriptedAgent runs the claudecode adapter against a real container: Prepare
// a workspace, materialise the fake agent, Launch with Bin pointed at it, and assert the
// session's activities and result arrive through the real exec streams.
func TestDockerSmokeScriptedAgent(t *testing.T) {
	sb, err := docker.NewSandbox("", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := sb.Available(ctx); err != nil {
		t.Fatalf("docker daemon not available: %v", err)
	}

	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID:     "run-smoke-" + domain.NewID(),
		ProjectID: "proj-s20-smoke",
		Files: map[string][]byte{
			".lexicode/fake-claude.sh": []byte(fakeClaude),
		},
		SetupScript: "chmod +x /workspace/.lexicode/fake-claude.sh",
		Network:     ports.NetworkPolicy{Mode: ports.NetworkOpen},
	}, nullProvisionSink{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if err := inst.Destroy(dctx); err != nil {
			t.Errorf("cleanup destroy: %v", err)
		}
	}()

	rt := claudecode.NewRuntime(claudecode.Options{Bin: "/workspace/.lexicode/fake-claude.sh"})
	sink := &countingSink{}
	h, err := rt.Launch(ctx, ports.RunSpec{
		RunID:  "run-smoke",
		Prompt: "say hello",
		Model:  "fake-model",
	}, inst, sink)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	res, err := h.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ExitCode != 0 || res.IsError || res.Stopped {
		t.Errorf("result = %+v, want a clean exit", res)
	}
	if res.ResultText != "smoke complete" {
		t.Errorf("ResultText = %q, want %q", res.ResultText, "smoke complete")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	var kinds []string
	okSeen := false
	for _, a := range sink.activities {
		kinds = append(kinds, string(a.Type)+":"+a.Title)
		if a.Type == domain.ActivityAction && a.OK != nil && *a.OK {
			okSeen = true
		}
	}
	t.Logf("activities through the docker path:\n  %v", kinds)
	if len(sink.activities) < 4 {
		t.Fatalf("activities = %v, want the full session", kinds)
	}
	if first, last := sink.activities[0], sink.activities[len(sink.activities)-1]; first.Title != "session started" ||
		last.Type != domain.ActivityResponse || !okSeen {
		t.Errorf("unexpected session shape: %v", kinds)
	}
}

// TestDockerAgentExitsWhenItsStdinCloses is the container-level regression test for the
// production hang: a run that had finished its work sat in `running` for thirty-five minutes
// because nothing closed the agent's stdin. The CLI never exited, its stdout never reached
// EOF, Wait never returned, and the scheduler's terminal path — push, pull request, ticket
// move — never ran.
//
// The fixture here behaves the way the real CLI does in `--input-format stream-json` mode: it
// emits a result and then blocks on stdin. Nothing kills it and nothing times it out, so the
// only thing that can end this test is the adapter closing stdin of its own accord. The
// marker file proves it exited on EOF rather than on a signal.
func TestDockerAgentExitsWhenItsStdinCloses(t *testing.T) {
	sb, err := docker.NewSandbox("", slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := sb.Available(ctx); err != nil {
		t.Fatalf("docker daemon not available: %v", err)
	}

	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID:     "run-eof-" + domain.NewID(),
		ProjectID: "proj-eof",
		Files: map[string][]byte{
			".lexicode/fake-claude.sh": []byte(fakeClaude),
		},
		SetupScript: "chmod +x /workspace/.lexicode/fake-claude.sh",
		Network:     ports.NetworkPolicy{Mode: ports.NetworkOpen},
	}, nullProvisionSink{})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if err := inst.Destroy(dctx); err != nil {
			t.Errorf("cleanup destroy: %v", err)
		}
	}()

	rt := claudecode.NewRuntime(claudecode.Options{Bin: "/workspace/.lexicode/fake-claude.sh"})
	h, err := rt.Launch(ctx, ports.RunSpec{
		RunID: "run-eof", Prompt: "do the work", Model: "fake-model",
	}, inst, &countingSink{})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Nobody stops this run and nobody kills it. If the session does not end on its own the
	// wait below runs out — which is exactly the production symptom.
	waitCtx, waitCancel := context.WithTimeout(ctx, 90*time.Second)
	defer waitCancel()
	start := time.Now()
	res, err := h.Wait(waitCtx)
	if err != nil {
		t.Fatalf("the session never ended by itself (%v) — the agent is still waiting on stdin, "+
			"which is the hang this test exists for", err)
	}
	t.Logf("the container agent exited %v after its result, exit code %d", time.Since(start), res.ExitCode)
	if res.ExitCode != 0 || res.IsError || res.Stopped {
		t.Errorf("result = %+v, want a clean, unstopped exit", res)
	}
	if res.ResultText != "smoke complete" {
		t.Errorf("ResultText = %q, want %q", res.ResultText, "smoke complete")
	}
	if _, err := inst.ReadFile(ctx, exitMarker); err != nil {
		t.Errorf("no %s in the container: the agent did not exit on stdin EOF (%v)", exitMarker, err)
	}
}
