package runner

import (
	"context"
	"log/slog"
	"strings"
	"time"

	actcontainer "gitea.com/gitea/runner/act/container"
	"github.com/moby/moby/client"
)

// sweeper removes the containers, volumes and networks a previous job left
// behind.
//
// act removes its own on the way out, but that path is Docker API calls, and
// when the daemon stops answering — which is exactly the failure that leaves a
// job hung until its timeout — the cleanup never runs. The leftovers then sit
// there holding disk until someone notices. A runner that restarts should not
// need a human to tidy up after it.
type sweeper struct {
	prefix string
	log    *slog.Logger
}

// sweep removes everything named with this runner's prefix.
//
// Scoped by prefix rather than by "anything that looks like ours" because two
// runners can share one daemon, and a sweep that took the other one's live
// container would be a worse bug than the leak it fixes.
func (s *sweeper) sweep(ctx context.Context, why string) {
	if s == nil || s.prefix == "" {
		return
	}
	// Bounded: the daemon being unresponsive is the very situation that
	// produced the leftovers, and blocking the runner on it helps nobody.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cli, err := actcontainer.GetDockerClient(ctx)
	if err != nil {
		s.log.Warn("cannot reach the docker daemon to sweep leftovers", "err", err)
		return
	}
	defer cli.Close()

	var containers, volumes, networks int
	if list, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true}); err == nil {
		for _, c := range list.Items {
			if !s.mine(c.Names...) {
				continue
			}
			if _, err := cli.ContainerRemove(ctx, c.ID,
				client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err == nil {
				containers++
			}
		}
	}
	// Volumes after containers: a volume still attached to a container cannot
	// be removed, and act names both from the same prefix.
	if vols, err := cli.VolumeList(ctx, client.VolumeListOptions{}); err == nil {
		for _, v := range vols.Items {
			if !s.mine(v.Name) {
				continue
			}
			if _, err := cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true}); err == nil {
				volumes++
			}
		}
	}
	if nets, err := cli.NetworkList(ctx, client.NetworkListOptions{}); err == nil {
		for _, n := range nets.Items {
			if !s.mine(n.Name) {
				continue
			}
			if _, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err == nil {
				networks++
			}
		}
	}
	if containers+volumes+networks > 0 {
		s.log.Info("swept leftovers from a previous job", "why", why,
			"containers", containers, "volumes", volumes, "networks", networks)
	}
}

// mine reports whether any of these names belongs to this runner. Docker
// returns container names with a leading slash; the other kinds do not.
func (s *sweeper) mine(names ...string) bool {
	for _, n := range names {
		if strings.HasPrefix(strings.TrimPrefix(n, "/"), s.prefix) {
			return true
		}
	}
	return false
}
