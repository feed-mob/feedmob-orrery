package runner

import (
	"net"
	"os"
	"path/filepath"
	"time"
)

// ResolveDockerHost finds the Docker daemon this runner should talk to.
//
// act's client defaults to /var/run/docker.sock, which is wrong on every
// rootless or VM-backed setup — Colima, Rancher Desktop and Docker Desktop all
// put the socket under the user's home. Rather than make every operator export
// DOCKER_HOST, we probe the conventional locations and report which one we
// picked, so a wrong guess is visible in the log instead of surfacing later as
// "cannot connect to the Docker daemon" in the middle of a job.
//
// An explicit DOCKER_HOST always wins.
func ResolveDockerHost() string {
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/var/run/docker.sock",
		filepath.Join(home, ".docker/run/docker.sock"),     // Docker Desktop
		filepath.Join(home, ".colima/default/docker.sock"), // Colima
		filepath.Join(home, ".rd/docker.sock"),             // Rancher Desktop
	}
	for _, path := range candidates {
		if dialable(path) {
			return "unix://" + path
		}
	}
	// Nothing answered. Return the conventional path so the resulting error
	// names something an operator can act on.
	return "unix:///var/run/docker.sock"
}

// dialable reports whether a unix socket exists and accepts a connection.
// Existence alone is not enough: a stopped VM leaves the socket file behind.
func dialable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
