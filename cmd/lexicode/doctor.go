package main

// doctor.go is `lexicode doctor` (S39): the pre-flight check a user runs when something does
// not work, and the first thing to run on a fresh machine. Every check answers one question
// a real failure raises, and every failure prints the fix on the next line — the command is
// worthless if it only says "no".
//
// It touches nothing: no migrations, no image builds, no containers, no writes. It opens the
// database read-only-ish (the store migrates nothing here), pings Docker, asks the credential
// sources whether they are healthy, and verifies each connected repository's token against
// the forge. Exit code is 1 if any check failed, 0 otherwise — warnings do not fail the run.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spruce/lexicode/internal/config"
	"github.com/spruce/lexicode/internal/domain"
	"github.com/spruce/lexicode/internal/kernel/ports"
	"github.com/spruce/lexicode/internal/kernel/secrets"
	"github.com/spruce/lexicode/internal/kernel/store"
	credentialsmod "github.com/spruce/lexicode/internal/module/credentials"
	dockermod "github.com/spruce/lexicode/internal/module/docker"
	githubmod "github.com/spruce/lexicode/internal/module/github"
)

// minFreeBytes is the disk-space floor. The agent base image is about 1.5 GB and every run gets a
// workspace volume; below this, a first run fails halfway through an image build, which is
// the worst possible moment to discover it.
const minFreeBytes = 5 << 30 // 5 GiB

// doctorTimeout bounds every network-ish check, so an unreachable daemon or a black-holed
// API cannot make the command hang.
const doctorTimeout = 15 * time.Second

// result is one check's outcome.
type result struct {
	name   string
	state  string // "ok" | "warn" | "FAIL"
	detail string
	fix    string // printed on failure (and on warnings that have an action)
}

func ok(name, detail string) result { return result{name: name, state: "ok", detail: detail} }
func warn(name, detail, fix string) result {
	return result{name: name, state: "warn", detail: detail, fix: fix}
}
func fail(name, detail, fix string) result {
	return result{name: name, state: "FAIL", detail: detail, fix: fix}
}

func cmdDoctor(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lexicode doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	config.Bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(config.Options{Flags: fs})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*doctorTimeout)
	defer cancel()

	fmt.Fprintf(stdout, "lexicode doctor — %s\n", versionString())
	fmt.Fprintf(stdout, "data dir: %s\n\n", cfg.DataDir)

	var results []result
	results = append(results, checkDataDir(cfg))
	results = append(results, checkDiskSpace(cfg))
	// Whether our own server is already up decides how to read a bound port: the API port is
	// identified directly, and the egress proxy port is expected to be held by the same
	// process (it has no probe of its own — it is an HTTP proxy, not an API).
	apiRunning := servingLexicode(cfg.Host, cfg.Port)
	results = append(results, checkPort("Port", cfg.Host, cfg.Port, "port", apiRunning))
	results = append(results, checkPort("Proxy port", "0.0.0.0", cfg.ProxyPort, "proxy-port", apiRunning))
	results = append(results, checkDocker(ctx, cfg)...)
	results = append(results, checkCredentialsAndRepos(ctx, cfg)...)

	width := 0
	for _, r := range results {
		if len(r.name) > width {
			width = len(r.name)
		}
	}
	failures := 0
	for _, r := range results {
		if r.state == "FAIL" {
			failures++
		}
		fmt.Fprintf(stdout, "  %-5s %-*s  %s\n", r.state, width, r.name, r.detail)
		if r.fix != "" && r.state != "ok" {
			for i, line := range strings.Split(r.fix, "\n") {
				lead := "fix: "
				if i > 0 {
					lead = "     "
				}
				fmt.Fprintf(stdout, "        %-*s  %s%s\n", width, "", lead, line)
			}
		}
	}
	fmt.Fprintln(stdout)
	if failures > 0 {
		return fmt.Errorf("%d of %d checks failed", failures, len(results))
	}
	fmt.Fprintf(stdout, "all %d checks passed\n", len(results))
	return nil
}

// ---------------------------------------------------------------- checks -----

func checkDataDir(cfg config.Config) result {
	const name = "Data dir"
	info, err := os.Stat(cfg.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return warn(name, cfg.DataDir+" does not exist yet",
			"`lexicode serve` creates it on first boot; nothing to do")
	}
	if err != nil {
		return fail(name, err.Error(), "check the path and its permissions")
	}
	if !info.IsDir() {
		return fail(name, cfg.DataDir+" is not a directory",
			"point --data-dir somewhere else, or move the file out of the way")
	}
	probe := filepath.Join(cfg.DataDir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fail(name, "not writable: "+err.Error(),
			fmt.Sprintf("chown/chmod %s so this user can write it", cfg.DataDir))
	}
	_ = os.Remove(probe)
	return ok(name, cfg.DataDir+" exists and is writable")
}

func checkDiskSpace(cfg config.Config) result {
	const name = "Disk space"
	dir := cfg.DataDir
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return warn(name, "cannot find an existing parent of "+cfg.DataDir, "")
		}
		dir = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return warn(name, "could not stat the filesystem: "+err.Error(), "")
	}
	free := uint64(st.Bavail) * uint64(st.Bsize) //nolint:unconvert,gosec // field widths differ per platform
	detail := fmt.Sprintf("%s free on %s", humanBytes(free), dir)
	if free < minFreeBytes {
		return fail(name, detail,
			fmt.Sprintf("free up disk: the agent image is about 1.5 GB and each run gets a workspace volume; "+
				"Lexicode wants at least %s", humanBytes(minFreeBytes)))
	}
	return ok(name, detail)
}

// checkPort reports whether the port can be bound. A port held by a Lexicode server that is
// already running is not a failure — that is the normal state while it serves — so the check
// works out who the occupant is before judging it.
func checkPort(name, host string, port int, flagName string, serverRunning bool) result {
	label := fmt.Sprintf("%s %d", name, port)
	ln, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err == nil {
		_ = ln.Close()
		return ok(label, "free")
	}
	if serverRunning {
		return ok(label, "in use by the Lexicode server already running on this data dir")
	}
	return fail(label, "in use by another process: "+err.Error(),
		fmt.Sprintf("stop whatever holds the port, or run with --%s <other>", flagName))
}

// servingLexicode probes for our own API on a port that refused to bind.
func servingLexicode(host string, port int) bool {
	if host == "0.0.0.0" || host == "" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: time.Second}
	url := fmt.Sprintf("http://%s/api/v1/auth/me", net.JoinHostPort(host, fmt.Sprint(port)))
	req, err := http.NewRequest(http.MethodGet, url, nil) //nolint:noctx // bounded by the client timeout
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	// Unauthenticated or set-up-required: either way it is our API answering.
	return resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK
}

func checkDocker(ctx context.Context, cfg config.Config) []result {
	const daemon = "Docker"
	const image = "Agent image"
	tag := dockermod.BuiltinImageTag()

	sb, err := dockermod.NewSandbox(cfg.DockerHost, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return []result{fail(daemon, err.Error(),
			"check docker_host / DOCKER_HOST; it must be a URL like unix:///var/run/docker.sock")}
	}
	dctx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	if err := sb.Available(dctx); err != nil {
		return []result{fail(daemon, err.Error(),
			"start Docker Desktop (or `sudo systemctl start docker`), then re-run.\n"+
				"If the daemon runs somewhere else, set docker_host in config.yaml or DOCKER_HOST.")}
	}
	out := []result{ok(daemon, "daemon reachable")}

	present, err := sb.HasImage(dctx, tag)
	switch {
	case err != nil:
		out = append(out, fail(image, err.Error(), "the daemon answered the ping but not the image list; check Docker's logs"))
	case present:
		out = append(out, ok(image, tag+" present"))
	default:
		out = append(out, warn(image, tag+" not built yet",
			"nothing to do — the first run builds it from the embedded Dockerfile (a few minutes)"))
	}
	return out
}

// checkCredentialsAndRepos opens the database and the secret store once, then answers the two
// questions that need them: can we authenticate to Claude, and does each connected repo's
// token still work?
func checkCredentialsAndRepos(ctx context.Context, cfg config.Config) []result {
	const claude = "Claude token"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := os.Stat(cfg.DBFile()); errors.Is(err, os.ErrNotExist) {
		return []result{warn(claude, "no database yet at "+cfg.DBFile(),
			"run `lexicode serve` once; then paste `claude setup-token` output into Settings → Credentials")}
	}
	st, err := store.Open(store.Options{Path: cfg.DBFile(), Logger: logger})
	if err != nil {
		return []result{fail(claude, "opening the database: "+err.Error(),
			"check that "+cfg.DBFile()+" is readable and not corrupt")}
	}
	defer func() { _ = st.Close() }()

	sec, err := secrets.Open(secrets.Options{Store: st, KeyPath: cfg.MasterKeyFile(), Logger: logger})
	if err != nil {
		return []result{fail(claude, "opening the secret store: "+err.Error(),
			"the master key file must exist and be mode 0600: "+cfg.MasterKeyFile())}
	}

	creds := credentialsmod.New(credentialsmod.Options{Secrets: sec})
	var out []result
	oauthErr := creds.OAuth().Health(ctx)
	envErr := creds.Env().Health(ctx)
	switch {
	case oauthErr == nil:
		out = append(out, ok(claude, "a stored OAuth token is present and well-formed"))
	case envErr == nil:
		out = append(out, warn(claude, "no stored token; falling back to the server's environment",
			"that works, but the product path is `claude setup-token` pasted into Settings → Credentials"))
	default:
		out = append(out, fail(claude, oauthErr.Error(),
			"run `claude setup-token` and paste the whole result into Settings → Credentials"))
	}

	out = append(out, checkRepoTokens(ctx, cfg, st, sec, logger)...)
	return out
}

func checkRepoTokens(ctx context.Context, cfg config.Config, st *store.Store, sec *secrets.Store, logger *slog.Logger) []result {
	repos, err := st.Repos().List(ctx)
	if err != nil {
		return []result{fail("GitHub tokens", err.Error(), "check the database")}
	}
	if len(repos) == 0 {
		return []result{ok("GitHub token", "no repository is connected yet")}
	}
	forge := githubmod.New(githubmod.Options{BaseURL: cfg.GitHubBaseURL, Logger: logger}).Forge()

	out := make([]result, 0, len(repos))
	for _, rp := range repos {
		name := "GitHub " + rp.Owner + "/" + rp.Name
		if rp.TokenSecretID == nil {
			out = append(out, fail(name, "no stored token",
				"reconnect the repository in project settings"))
			continue
		}
		token, err := sec.Get(ctx, *rp.TokenSecretID)
		if err != nil {
			out = append(out, fail(name, "reading the stored token: "+err.Error(),
				"reconnect the repository in project settings"))
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, doctorTimeout)
		info, err := forge.Verify(vctx, ports.Creds{Token: token}, rp.Ref())
		cancel()
		out = append(out, repoVerdict(name, rp, info, err))
	}
	return out
}

// repoVerdict turns a Verify outcome into a check line — the three failure modes the
// troubleshooting doc names (expired, missing scope, rate limited) each get their own fix.
func repoVerdict(name string, rp domain.Repo, info ports.RepoInfo, err error) result {
	var scope *ports.MissingScopeError
	var limited *ports.RateLimitedError
	switch {
	case err == nil:
		return ok(name, fmt.Sprintf("token valid; default branch %s", info.DefaultBranch))
	case errors.As(err, &scope):
		return fail(name, err.Error(),
			"issue a new token with the "+scope.Scope+" scope and reconnect the repository")
	case errors.As(err, &limited):
		return warn(name, err.Error(),
			"wait for the reset; Lexicode backs off on its own and keeps serving")
	case strings.Contains(err.Error(), "401"):
		return fail(name, "the token was rejected (401): "+err.Error(),
			"the token expired or was revoked — issue a new one and reconnect the repository")
	default:
		return fail(name, err.Error(),
			"check network access to the GitHub API and the repository's visibility")
	}
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
