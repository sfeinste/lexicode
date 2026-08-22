package docker

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

// dockerfile is the agent base image definition, embedded in the binary (D-7): no registry
// dependency, no publishing pipeline. See the Dockerfile itself for what is inside and why the
// Node install is pinned the way it is.
//
//go:embed Dockerfile
var dockerfile []byte

// imageRepo is the local repository the built-in image is tagged under.
const imageRepo = "lexicode/agent-base"

// BuiltinImageTag is the tag the embedded Dockerfile builds to:
// lexicode/agent-base:<sha256 of the Dockerfile, first 12 hex chars>. The tag is a content
// hash, so upgrading the binary rebuilds exactly when the Dockerfile changed and never
// otherwise (D-7).
func BuiltinImageTag() string {
	sum := sha256.Sum256(dockerfile)
	return imageRepo + ":" + hex.EncodeToString(sum[:])[:12]
}
