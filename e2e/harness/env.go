package harness

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the drivers reach the repos row the API does not expose yet
)

// FindRepoRoot walks up from the working directory to the module root.
func FindRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s; run from inside the repo", dir)
		}
		dir = parent
	}
}

// DetectHostIP finds a non-loopback IPv4 the Docker VM can dial — the fake GitHub must be
// reachable both from this process (forge API calls) and from inside containers (git).
func DetectHostIP() (string, error) {
	for _, iface := range []string{"en0", "en1"} {
		out, err := exec.Command("ipconfig", "getifaddr", iface).Output()
		if err == nil && len(bytes.TrimSpace(out)) > 0 {
			return string(bytes.TrimSpace(out)), nil
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if v4 := ipNet.IP.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found; containers could not reach the fake GitHub")
}

// WaitFor polls cond until it is true or the timeout expires.
func WaitFor(what string, timeout time.Duration, cond func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := cond()
		if err != nil {
			return fmt.Errorf("waiting for %s: %w", what, err)
		}
		if ok {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for %s", timeout, what)
}

// Compact renders a value as one line of JSON, for failure messages.
func Compact(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// PrefixWriter returns a writer that prefixes every line, so subprocess output stays readable
// inside the harness log.
func PrefixWriter(prefix string) io.Writer {
	pr, pw := io.Pipe()
	go func() {
		buf := make([]byte, 4096)
		line := []byte{}
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				line = append(line, buf[:n]...)
				for {
					i := bytes.IndexByte(line, '\n')
					if i < 0 {
						break
					}
					fmt.Printf("%s%s\n", prefix, line[:i])
					line = line[i+1:]
				}
			}
			if err != nil {
				if len(line) > 0 {
					fmt.Printf("%s%s\n", prefix, line)
				}
				return
			}
		}
	}()
	return pw
}

// ---------------------------------------------------------------- images -----

// BuildAgentImage makes sure the built-in agent base image exists (building it from the
// embedded Dockerfile's source if not) and bakes a scripted `claude` into a derived image at
// /usr/local/bin/claude. repos.image_ref then points the runtime at the derived tag.
func BuildAgentImage(repoRoot, tag, claudeScript string) error {
	dockerfilePath := filepath.Join(repoRoot, "internal", "module", "docker", "Dockerfile")
	raw, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	baseTag := "lexicode/agent-base:" + hex.EncodeToString(sum[:])[:12]

	if !ImageExists(baseTag) {
		log.Printf("building the agent base image %s (first run can take minutes)…", baseTag)
		cmd := exec.Command("docker", "build", "-t", baseTag,
			"-f", dockerfilePath, filepath.Dir(dockerfilePath))
		cmd.Stdout = PrefixWriter("docker| ")
		cmd.Stderr = PrefixWriter("docker| ")
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("building base image: %w", err)
		}
	} else {
		log.Printf("agent base image %s present", baseTag)
	}

	ctxDir, err := os.MkdirTemp("", "lexicode-e2e-image-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()
	if err := os.WriteFile(filepath.Join(ctxDir, "claude"), []byte(claudeScript), 0o755); err != nil { //nolint:gosec // an executable fixture script
		return err
	}
	dockerfile := "FROM " + baseTag + "\nCOPY --chmod=0755 claude /usr/local/bin/claude\n"
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil { //nolint:gosec // fixture
		return err
	}
	cmd := exec.Command("docker", "build", "-t", tag, ctxDir)
	cmd.Stdout = PrefixWriter("docker| ")
	cmd.Stderr = PrefixWriter("docker| ")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building derived image: %w", err)
	}
	log.Printf("derived agent image %s built (scripted claude at /usr/local/bin/claude)", tag)
	return nil
}

// ImageExists reports whether the local daemon has an image.
func ImageExists(tag string) bool {
	return exec.Command("docker", "image", "inspect", tag).Run() == nil
}

// ---------------------------------------------------------------- the repos row -----

// SetRepoSettings writes image_ref and network_policy straight to the repos row: the two
// settings with no API surface yet. The server is running; SQLite WAL plus busy_timeout make
// a second-process write safe. `open` keeps the fixture on the bridge network, where both the
// fake GitHub and the MCP endpoint are reachable without the egress proxy in the path.
func SetRepoSettings(dbPath, projectKey, imageRef string) error {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	res, err := db.Exec(
		`UPDATE repos SET image_ref = ?, network_policy = 'open'
		 WHERE project_id = (SELECT id FROM projects WHERE key = ?)`, imageRef, projectKey)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("repos settings update touched %d rows, want 1", n)
	}
	return nil
}

// SetPollInterval writes workspace_settings.poll_interval_seconds. The poller floors it at 10
// seconds (architecture §7) whatever is stored — the drivers set it so the chain moves at the
// floor instead of the 30-second default, and say so.
func SetPollInterval(dbPath string, seconds int) error {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`UPDATE workspace_settings SET poll_interval_seconds = ?`, seconds); err != nil {
		return err
	}
	return nil
}
