package docker

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/spruce/lexicode/internal/domain"
)

// Instance ownership. `lexicode.instance` identifies one container (it is the InstanceRef the
// scheduler reattaches by), so it says nothing about *which* Lexicode is running it: several
// Lexicodes on one machine — the product plus an acceptance run, two workspaces, a demo — all
// stamp it, and a sweep keyed on its mere presence reaps the others' live containers.
//
// The owner label is the missing half: the identity of the process, stable across its own
// restarts so that a boot sweep still recognises the containers its previous incarnation left
// behind. A Lexicode instance *is* its workspace — one data directory, one database, one set of
// runs — so the data directory is the identity, hashed to keep an operator's paths out of
// `docker inspect` output and to keep the value a fixed, label-safe length.
//
// Containers stamped by a Lexicode that predates this label carry no owner at all. They are
// left alone rather than swept: an unowned container is exactly as likely to belong to another
// instance as to this one, and leaking a stopped container costs disk, while reaping a live one
// costs a run.
func ownerID(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	path := filepath.Clean(dataDir)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	sum := sha256.Sum256([]byte(path))
	return "ws-" + hex.EncodeToString(sum[:])[:16]
}

// processOwner is the owner identity of a Sandbox built without a data directory: doctor, the
// docker-tagged tests, any direct NewSandbox caller. It is per-process, so such a Sandbox still
// only ever sweeps containers it created itself — the safe half of the guarantee — but it does
// not survive a restart, so a process using it cannot clean up after its own crash. Production
// wiring passes Options.DataDir and gets the stable identity instead.
var processOwner = "proc-" + strings.ToLower(domain.NewID())
