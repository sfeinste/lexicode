//go:build docker

// The credential-boundary acceptance: for the whole life of the agent's process, the
// container holds nothing that can write to the repository — and the orchestrator can still
// push afterwards.
//
// This is the test the whole change exists for, so it is deliberately literal. It stands up a
// real git smart-HTTP server that DEMANDS the token (a 401 without it, exactly like a private
// repository), clones through it with the tokenized URL, and then looks for that token
// everywhere a leak could hide while a stand-in agent process is running:
//
//   - the agent process's own environment, read out of /proc/<pid>/environ inside the
//     container — the thing a root agent would actually read;
//   - every OTHER process's environment too, so a leak into the container's Config.Env is
//     caught even if it never reached the agent;
//   - .git/config, git remote -v, and a recursive grep of the whole .git directory;
//   - the environment of a fresh exec, which is what any tool the agent starts inherits.
//
// Then it proves the two halves of "by construction": the container's own `git push` is
// refused by the server, and the orchestrator's push — the token supplied through
// GIT_CONFIG_*/http.extraheader in that one exec's environment — succeeds.
package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/sched"
)

// fixtureToken is the stand-in PAT. It is deliberately long and unmistakable so that a grep
// for it cannot match anything by accident.
const fixtureToken = "ghp_FIXTURE0000token0000never0000real0000"

// authedGitServer serves `git http-backend` over HTTP and refuses every request that does not
// present the token as HTTP basic auth — the private-repository behaviour the whole design
// turns on. It listens on all interfaces so the container can reach it through
// host.docker.internal, and returns its base URL as the container sees it.
func authedGitServer(t *testing.T, root string) (base string, unauthorized *int) {
	t.Helper()
	execPath, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	backend := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(string(execPath)), "git-http-backend"),
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	// The auth-scheme token is case-insensitive (RFC 7235), and the two callers spell it
	// differently: git's own URL-credential retry sends "Basic", while the `http.extraheader`
	// form GitHub documents — and that the orchestrator uses — sends "basic". Both must be
	// accepted, or the fixture would be testing its own spelling.
	want := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + fixtureToken))
	authorized := func(h string) bool {
		scheme, cred, ok := strings.Cut(h, " ")
		return ok && strings.EqualFold(scheme, "basic") && strings.TrimSpace(cred) == want
	}
	refused := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r.Header.Get("Authorization")) {
			refused++
			t.Logf("git server: 401 %s %s (auth=%q)", r.Method, r.URL.Path,
				redactAuth(r.Header.Get("Authorization")))
			w.Header().Set("WWW-Authenticate", `Basic realm="lexicode-fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	})

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{
		Handler: h, ReadHeaderTimeout: 10 * time.Second,
	}}
	srv.Start()
	t.Cleanup(srv.Close)
	port := ln.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://host.docker.internal:%d", port), &refused
}

// redactAuth keeps the log readable without printing a credential, even a fake one.
func redactAuth(v string) string {
	if v == "" {
		return "<none>"
	}
	return "<present>"
}

// servedRepo builds <dir>/fixture.git with one commit on main and receive-pack enabled, and
// returns the directory to serve and the repository's path component.
func servedRepo(t *testing.T) (root, path string) {
	t.Helper()
	root = t.TempDir()
	bare := filepath.Join(root, "fixture.git")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`set -e
git init -q --initial-branch=main %[1]s
cd %[1]s
git config user.email fixture@test
git config user.name Fixture
echo "hello fixture" > README.md
git add -A
git commit -q -m "initial commit"
git init -q --bare --initial-branch=main %[2]s
git --git-dir %[2]s config http.receivepack true
git push -q %[2]s main
`, work, bare)
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("building fixture repo: %v\n%s", err, out)
	}
	return root, "fixture.git"
}

func TestCredentialNeverCoexistsWithTheAgent(t *testing.T) {
	root, repoPath := servedRepo(t)
	base, refused := authedGitServer(t, root)
	sb := newTestSandbox(t)
	sink := newTestSink(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	tokenized := fmt.Sprintf("http://x-access-token:%s@%s/%s",
		fixtureToken, strings.TrimPrefix(base, "http://"), repoPath)

	inst, err := sb.Prepare(ctx, ports.SandboxSpec{
		RunID: "cred-run", ProjectID: "cred-project",
		Clone: ports.CloneSpec{
			URL: tokenized, Ref: "main", Branch: "dev/cred-check",
			UserName: "Dev", UserEmail: "dev@agents.lexicode.local",
		},
		// Exactly what the S19 builder puts in a real container: no repository credential
		// anywhere in it. If a future change smuggles one in here, the /proc sweep below
		// catches it.
		Env: map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-not-the-repo-token",
			"GIT_AUTHOR_NAME":         "Dev",
			"GIT_AUTHOR_EMAIL":        "dev@agents.lexicode.local",
		},
	}, sink)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	defer destroyQuietly(t, inst)

	// The clone itself proves the server really does demand the token: an unauthenticated
	// fetch could not have produced a checkout.
	if sink.state("clone") != ports.StepOK {
		t.Fatalf("clone step = %s, want ok", sink.state("clone"))
	}
	if code, out := execOutput(t, inst, "cat", "README.md"); code != 0 ||
		!strings.Contains(out, "hello fixture") {
		t.Fatalf("workspace has no checkout: exit %d\n%s", code, out)
	}
	t.Logf("clone through the authenticated remote succeeded; the server refused %d "+
		"unauthenticated request(s) along the way", *refused)

	// ---- a stand-in agent process, alive for the duration of the checks ----
	//
	// Started from an exec with no extra environment, exactly like the claudecode adapter
	// launches `claude`: whatever it can see is whatever the container itself carries.
	code, out := execOutput(t, inst, "/bin/sh", "-c",
		`nohup sleep 600 >/dev/null 2>&1 & echo $!`)
	if code != 0 {
		t.Fatalf("starting the stand-in agent: exit %d\n%s", code, out)
	}
	agentPID := strings.TrimSpace(out)
	if agentPID == "" {
		t.Fatal("no pid for the stand-in agent")
	}
	t.Logf("stand-in agent running as pid %s", agentPID)

	// ---- the core property, checked from inside the container ----
	checks := []struct {
		name string
		argv []string
	}{
		{
			"the agent process's own environment",
			[]string{"/bin/sh", "-c",
				`tr '\0' '\n' < /proc/` + agentPID + `/environ | grep -F "` + fixtureToken + `" || true`},
		},
		{
			"every process's environment",
			[]string{"/bin/sh", "-c",
				`cat /proc/[0-9]*/environ 2>/dev/null | tr '\0' '\n' | grep -F "` + fixtureToken + `" || true`},
		},
		{
			"a fresh exec's environment",
			[]string{"/bin/sh", "-c", `env | grep -F "` + fixtureToken + `" || true`},
		},
		{
			"git remote -v",
			[]string{"/bin/sh", "-c", `git remote -v | grep -F "` + fixtureToken + `" || true`},
		},
		{
			"remote.origin.url",
			[]string{"/bin/sh", "-c",
				`git config --get remote.origin.url | grep -F "` + fixtureToken + `" || true`},
		},
		{
			"anything under .git",
			[]string{"/bin/sh", "-c",
				`grep -r -l -F "` + fixtureToken + `" .git 2>/dev/null || true`},
		},
		{
			"anything under the workspace",
			[]string{"/bin/sh", "-c",
				`grep -r -l -F "` + fixtureToken + `" . 2>/dev/null || true`},
		},
	}
	for _, c := range checks {
		code, out := execOutput(t, inst, c.argv...)
		if code != 0 {
			t.Fatalf("%s: probe exited %d\n%s", c.name, code, out)
		}
		if strings.TrimSpace(out) != "" {
			t.Errorf("THE REPOSITORY CREDENTIAL IS VISIBLE IN %s:\n%s", c.name, out)
			continue
		}
		t.Logf("clean: %s", c.name)
	}

	// The remote still names the right repository — the credential was removed, not the
	// remote.
	_, originURL := execOutput(t, inst, "git", "config", "--get", "remote.origin.url")
	originURL = strings.TrimSpace(originURL)
	if originURL != base+"/"+repoPath {
		t.Fatalf("origin = %q, want the tokenless URL %q", originURL, base+"/"+repoPath)
	}
	t.Logf("origin is %s", originURL)

	// The clone step also fetched remote-tracking refs while the credential was still live.
	// That is what makes the tokenless remote survivable: a run that has to work on another
	// branch still has it locally, and the teardown push can tell this run's commits from
	// ones the remote already has.
	code, out = execOutput(t, inst, "git", "rev-parse", "--verify", "refs/remotes/origin/main")
	if code != 0 {
		t.Fatalf("no remote-tracking ref for main after the clone: exit %d\n%s", code, out)
	}
	t.Logf("refs/remotes/origin/main is %s", strings.TrimSpace(out))

	// ---- the agent cannot push, by construction ----
	code, out = execOutput(t, inst, "/bin/sh", "-c",
		`git commit -q --allow-empty -m "agent work" && git push origin HEAD:refs/heads/dev/cred-check 2>&1`)
	if code == 0 {
		t.Fatalf("the container pushed without a credential; the remote is not enforcing:\n%s", out)
	}
	if !strings.Contains(out, "401") && !strings.Contains(strings.ToLower(out), "authentication") {
		t.Logf("note: the push failed for a reason other than auth:\n%s", out)
	}
	t.Logf("the container's own push was refused, as designed:\n%s", strings.TrimSpace(out))
	if branchExists(t, filepath.Join(root, repoPath), "dev/cred-check") {
		t.Fatal("the refused push landed on the remote anyway")
	}

	// ---- the orchestrator's push, with the credential in that exec only ----
	pushEnv := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "http.extraheader",
		"GIT_CONFIG_VALUE_0": sched.BasicAuthHeader("x-access-token", fixtureToken),
	}
	st, err := inst.Exec(ctx, []string{"/bin/sh", "-c",
		`git push origin HEAD:refs/heads/dev/cred-check 2>&1`}, ports.ExecOpts{Env: pushEnv})
	if err != nil {
		t.Fatalf("orchestrator push exec: %v", err)
	}
	_ = st.Stdin.Close()
	pushOut := drainStreams(st)
	pcode, err := st.Wait()
	if err != nil {
		t.Fatalf("orchestrator push wait: %v", err)
	}
	if pcode != 0 {
		t.Fatalf("the orchestrator's push failed (exit %d):\n%s", pcode, pushOut)
	}
	t.Logf("orchestrator push:\n%s", strings.TrimSpace(pushOut))

	if !branchExists(t, filepath.Join(root, repoPath), "dev/cred-check") {
		t.Fatal("the orchestrator's push reported success but the branch is not on the remote")
	}

	// And the credential did not outlive the command that needed it.
	code, out = execOutput(t, inst, "/bin/sh", "-c",
		`{ git config --get remote.origin.url; git config --get http.extraheader; `+
			`tr '\0' '\n' < /proc/`+agentPID+`/environ; } 2>/dev/null | grep -F "`+
			fixtureToken+`" || true`)
	if code != 0 {
		t.Fatalf("post-push probe exited %d\n%s", code, out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("the push left the credential behind in the container:\n%s", out)
	}
	t.Log("after the push, the container still holds no credential")
}

// drainStreams reads both halves of an attached exec to EOF, concurrently (the demultiplexer
// deadlocks if one side is left unread), and returns the combined output.
func drainStreams(st ports.Streams) string {
	var (
		mu  sync.Mutex
		buf strings.Builder
		wg  sync.WaitGroup
	)
	for _, r := range []io.Reader{st.Stdout, st.Stderr} {
		if r == nil {
			continue
		}
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			b, _ := io.ReadAll(r)
			mu.Lock()
			buf.Write(b)
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	return buf.String()
}

// branchExists reports whether the served bare repository has the branch.
func branchExists(t *testing.T, bare, name string) bool {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "branch", "--list", name).Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	return strings.TrimSpace(string(out)) != ""
}
