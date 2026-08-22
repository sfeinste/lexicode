package docker

// The egress relay: the bridge between lexicode-internal and the host-side proxy (see the
// package comment in proxy.go for why containers on an internal network cannot reach the host
// directly — measured, not assumed). One container, `lexicode-egress`, attached to both the
// default bridge (where host.docker.internal works) and lexicode-internal (where agent
// containers resolve it by name), running a dumb TCP forwarder pinned to
// host.docker.internal:<proxy port>.
//
// The relay enforces nothing and can leak nothing: every byte it forwards lands on the
// orchestrator's proxy, which refuses anything without a registered run credential. It runs
// the built-in agent image (node is already there — no extra pull), carries the
// lexicode.egress label rather than lexicode.instance (the orphan sweeper must never reap
// it), and is recreated when the proxy port it targets changes.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/spruce/lexicode/internal/kernel/ports"
)

const (
	// relayContainerName is both the container name and the hostname agent containers dial —
	// Docker's embedded DNS resolves it on lexicode-internal.
	relayContainerName = "lexicode-egress"
	// relayPort is the port the forwarder listens on inside its own namespace.
	relayPort = 3128
	// labelEgress marks the relay container as ours without making it sweepable.
	labelEgress = "lexicode.egress"
	// labelEgressPort records which host proxy port the relay targets, so a config change is
	// noticed and the relay recreated.
	labelEgressPort = "lexicode.egress.port"
)

// relayScript is the whole forwarder. Plain node, no dependencies: accept, connect to the
// host proxy, pipe both ways.
const relayScript = `
const net = require("net");
const target = process.env.RELAY_TARGET_HOST;
const targetPort = Number(process.env.RELAY_TARGET_PORT);
net.createServer((c) => {
  const u = net.connect(targetPort, target);
  c.pipe(u); u.pipe(c);
  c.on("error", () => u.destroy());
  u.on("error", () => c.destroy());
}).listen(Number(process.env.RELAY_PORT), "0.0.0.0", () => console.log("lexicode egress relay up"));
`

// nopSink swallows provisioning progress: the relay's inner image ensure must not repaint the
// run's own "image" checklist step, which already completed.
type nopSink struct{}

func (nopSink) Step(string, ports.StepState, string) {}
func (nopSink) Log(string)                           {}

// ensureRelay makes the egress relay exist, run, and target proxyPort. Called from
// prepareNetwork for every none/allowlist container; idempotent and cheap when the relay is
// already right. A name-conflict race with a concurrent Prepare is resolved by re-inspecting.
func (s *Sandbox) ensureRelay(ctx context.Context, proxyPort int) error {
	args := filters.NewArgs(filters.Arg("name", relayContainerName))
	list, err := s.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return fmt.Errorf("docker: listing egress relay: %w", err)
	}
	for _, c := range list {
		if !hasName(c.Names, relayContainerName) {
			continue
		}
		if c.Labels[labelEgressPort] == strconv.Itoa(proxyPort) && c.State == container.StateRunning {
			return nil // already right
		}
		// Wrong target port, or stopped: recreate from scratch.
		err := s.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			return fmt.Errorf("docker: removing stale egress relay: %w", err)
		}
	}
	return s.createRelay(ctx, proxyPort)
}

func (s *Sandbox) createRelay(ctx context.Context, proxyPort int) error {
	// The relay runs the built-in image (node is guaranteed there); build it if this is a
	// custom-image workspace where nothing has built it yet.
	imageRef, err := s.ensureImage(ctx, "", nopSink{})
	if err != nil {
		return fmt.Errorf("docker: ensuring relay image: %w", err)
	}

	cfg := &container.Config{
		Image: imageRef,
		Cmd:   []string{"node", "-e", relayScript},
		Env: []string{
			"RELAY_PORT=" + strconv.Itoa(relayPort),
			"RELAY_TARGET_HOST=host.docker.internal",
			"RELAY_TARGET_PORT=" + strconv.Itoa(proxyPort),
		},
		Labels: map[string]string{
			labelEgress:     "1",
			labelEgressPort: strconv.Itoa(proxyPort),
		},
	}
	hostCfg := &container.HostConfig{
		ReadonlyRootfs: true,
		Init:           boolPtr(true),
		NetworkMode:    "bridge",
		// host-gateway makes host.docker.internal resolve on native Linux too, where Docker
		// Desktop's built-in alias does not exist.
		ExtraHosts:    []string{"host.docker.internal:host-gateway"},
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
	}
	created, err := s.cli.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{}, nil, relayContainerName)
	if err != nil {
		if cerrdefs.IsConflict(err) {
			return nil // lost the creation race; the winner's relay serves everyone
		}
		return fmt.Errorf("docker: creating egress relay: %w", err)
	}
	// Second leg: the internal network, where agent containers reach it by name.
	if err := s.cli.NetworkConnect(ctx, internalNetwork, created.ID, nil); err != nil {
		_ = s.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("docker: attaching egress relay to %s: %w", internalNetwork, err)
	}
	if err := s.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = s.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("docker: starting egress relay: %w", err)
	}
	return nil
}

// hasName matches Docker's leading-slash container name list against a plain name.
func hasName(names []string, want string) bool {
	for _, n := range names {
		if strings.TrimPrefix(n, "/") == want {
			return true
		}
	}
	return false
}
