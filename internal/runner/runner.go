package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
)

// Options configure a runner.
type Options struct {
	Name   string
	Labels Labels
	// WorkDir is where job workspaces and the action cache live.
	WorkDir string
	// HeartbeatInterval is how often a running task reports in. The reply to
	// the heartbeat is also how a stop request reaches us, so this doubles as
	// the upper bound on how long we keep working after someone asks us not to.
	HeartbeatInterval time.Duration
	// LogFlushInterval bounds how long a line waits before shipping.
	LogFlushInterval time.Duration
	// ActionsURL is where `uses: owner/repo@ref` resolves from. Pointing this
	// at a mirror instead of github.com is the whole of OQ-4.
	ActionsURL string
	// ActionsOffline serves only already-cached actions and refuses fetches.
	ActionsOffline bool
	// DockerHost is the daemon container jobs run on. Empty means probe the
	// conventional locations.
	DockerHost string
	// MountDockerSocket bind-mounts that daemon into every job container, so a
	// step can run `docker`. It is an escape hatch out of the job's own
	// sandbox, so it is off unless an operator asks for it.
	MountDockerSocket bool

	// ServiceAddr is the address job containers reach the artifact and cache
	// servers on. Empty probes the host's outbound IP, which is what a
	// container can route to; loopback is not.
	ServiceAddr  string
	ArtifactPort int
	CachePort    int
	// NoArtifacts and NoCache switch off the two servers. Off means
	// actions/upload-artifact and actions/cache fail rather than no-op, so
	// these exist for a runner whose jobs are known not to use them.
	NoArtifacts bool
	NoCache     bool
}

func (o *Options) withDefaults() {
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 3 * time.Second
	}
	if o.LogFlushInterval <= 0 {
		o.LogFlushInterval = time.Second
	}
	if o.WorkDir == "" {
		o.WorkDir = filepath.Join(os.TempDir(), "orrery-work")
	}
	if o.ActionsURL == "" {
		o.ActionsURL = "https://github.com"
	}
	if o.DockerHost == "" {
		o.DockerHost = ResolveDockerHost()
	}
	// Port 0 lets the OS choose for the cache server, which is what act does.
	// The artifact server needs a fixed port because its URL is built from the
	// configured value rather than from the listener.
	if o.ArtifactPort == 0 {
		o.ArtifactPort = 34567
	}
}

// Version is what this runner advertises.
const Version = "0.2.0-p0b"

// Capabilities are the flags we advertise at registration. Advertising
// "cancelling" is a promise: we will notice a stop request, wind down, and say
// so. A runner that cannot keep that promise must not claim it, because the
// server's only alternative is force-termination with cleanup unrun.
var Capabilities = []string{protocol.CapabilityCancelling}

// Runner polls for work and executes it.
type Runner struct {
	cl   *Client
	opts Options
	log  *slog.Logger
	exec *actExecutor
	svc  *services
}

// New builds a runner.
func New(cl *Client, opts Options, log *slog.Logger) *Runner {
	opts.withDefaults()
	// act's container client reads DOCKER_HOST from the process environment,
	// not from its Config — the Config's socket path is for mounting the
	// daemon *into* a container, which is a different question. So the only
	// way to point act at a VM-backed daemon is to export it here, once.
	if os.Getenv("DOCKER_HOST") == "" && opts.DockerHost != "" {
		_ = os.Setenv("DOCKER_HOST", opts.DockerHost)
	}
	return &Runner{
		cl:   cl,
		opts: opts,
		log:  log,
		exec: &actExecutor{
			labels:     opts.Labels,
			actionsURL: opts.ActionsURL,
			offline:    opts.ActionsOffline,
			cacheDir:   defaultCacheDir(opts.WorkDir),
			dockerHost: opts.DockerHost,

			mountDaemonSocket: opts.MountDockerSocket,
		},
	}
}

// Run loops until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	svc, err := startServices(ctx, r.opts, r.log)
	if err != nil {
		return err
	}
	r.svc = svc
	r.exec.artifacts = svc
	defer svc.close()

	names := r.opts.Labels.Names()
	if _, err = r.cl.Declare(ctx, &protocol.DeclareRequest{
		Version: Version, Labels: names, Capabilities: Capabilities,
	}); err != nil {
		return fmt.Errorf("declare: %w", err)
	}
	r.log.Info("runner ready", "name", r.opts.Name, "labels", names,
		"capabilities", Capabilities, "actions_url", r.opts.ActionsURL,
		"docker_host", r.opts.DockerHost)

	var version int64
	for {
		if ctx.Err() != nil {
			return nil
		}
		res, err := r.cl.FetchTask(ctx, &protocol.FetchTaskRequest{TasksVersion: version})
		if errors.Is(err, ErrUnauthorized) {
			return err
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.log.Warn("fetch failed, backing off", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		version = res.TasksVersion
		if res.Task == nil {
			continue
		}
		if err := r.execute(ctx, res.Task); err != nil {
			r.log.Error("task execution failed", "task", res.Task.ID, "err", err)
		}
	}
}

// execute runs one task to completion, streaming logs and heartbeating.
func (r *Runner) execute(ctx context.Context, task *protocol.Task) error {
	r.log.Info("task started", "task", task.ID, "timeout_min", task.TimeoutMinutes)

	workdir := filepath.Join(r.opts.WorkDir, fmt.Sprintf("task-%d", task.ID))
	defer os.RemoveAll(workdir)

	logs := newLogShipper(r.cl, task.ID, r.opts.LogFlushInterval, r.log)

	// stopCtx is what the job runs under. The heartbeat cancels it when the
	// server says stop; the cleanup below deliberately does NOT run under it,
	// so a stop cannot interrupt the winding-down it just asked for.
	stopCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()

	timeout := time.Duration(task.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = time.Hour
	}
	stopCtx, cancelTimeout := context.WithTimeout(stopCtx, timeout)
	defer cancelTimeout()

	var (
		mu      sync.Mutex
		stopped bool
		stopAt  time.Time
	)
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go func() {
		t := time.NewTicker(r.opts.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
			}
			res, err := r.cl.UpdateTask(ctx, &protocol.UpdateTaskRequest{
				State: &protocol.TaskState{ID: task.ID},
			})
			if err != nil || !res.StopRequested {
				continue
			}
			mu.Lock()
			already := stopped
			stopped = true
			if !already {
				stopAt = time.Now().UTC()
			}
			mu.Unlock()
			if !already {
				r.log.Info("stop requested by server, winding down", "task", task.ID)
				logs.write("::orrery:: stop requested — winding down")
				cancelJob()
			}
		}
	}()

	result, execErr := r.exec.run(stopCtx, task, workdir, logs)
	stopHeartbeat()

	mu.Lock()
	wasStopped, stoppedAt := stopped, stopAt
	mu.Unlock()

	// Cleanup runs under the parent context, never under stopCtx: the whole
	// point of asking a runner to stop is that it gets to finish tidying up.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelCleanup()

	final := protocol.ResultFailure
	var steps []protocol.StepState
	var outputs map[string]string
	if result != nil {
		final, steps, outputs = result.Result, result.Steps, result.Outputs
	}
	// Always surface the execution error. Reporting only the hook's verdict
	// leaves a failed job with an empty log and no reason, which is the exact
	// "log visibility" complaint we set out not to reproduce.
	//
	// A stop is the exception: "context canceled" is then the thing we were
	// asked to do, and calling it an error teaches readers to ignore ::error::.
	if execErr != nil {
		switch {
		case wasStopped && errors.Is(execErr, context.Canceled):
			logs.write("::orrery:: execution ended on the stop request")
		default:
			logs.write(fmt.Sprintf("::error::%v", execErr))
			r.log.Error("job execution returned an error", "task", task.ID, "err", execErr)
		}
	}
	if wasStopped {
		final = protocol.ResultCancelled
		steps = markCancelled(steps, stoppedAt)
	}

	var ackedAt *time.Time
	if wasStopped {
		logs.write("::orrery:: cleanup complete, acknowledging stop")
		now := time.Now().UTC()
		ackedAt = &now
	}
	logs.close(cleanupCtx)

	r.log.Info("task complete", "task", task.ID, "result", final, "stopped", wasStopped, "steps", len(steps))
	r.finish(cleanupCtx, task.ID, final, steps, outputs, ackedAt)
	return execErr
}

// markCancelled relabels the steps a stop interrupted. act has no cancelled
// outcome for a step: a context cancellation surfaces as a plain failure, so a
// job everyone agreed to stop would otherwise read as a job that broke. Steps
// that had already settled before the stop keep whatever they settled as — a
// genuine failure does not become a cancellation because a stop followed it.
func markCancelled(steps []protocol.StepState, stoppedAt time.Time) []protocol.StepState {
	for i := range steps {
		st := &steps[i]
		switch {
		case st.Result == protocol.ResultUnspecified && st.StartedAt != nil:
			st.Result = protocol.ResultCancelled
		case st.Result == protocol.ResultFailure &&
			(st.StoppedAt == nil || !st.StoppedAt.Before(stoppedAt)):
			st.Result = protocol.ResultCancelled
		}
	}
	return steps
}

func (r *Runner) finish(ctx context.Context, id int64, result protocol.Result, steps []protocol.StepState, outputs map[string]string, acked *time.Time) {
	now := time.Now().UTC()
	if _, err := r.cl.UpdateTask(ctx, &protocol.UpdateTaskRequest{
		State: &protocol.TaskState{
			ID: id, Result: result, StoppedAt: &now, StopAckedAt: acked, Steps: steps,
		},
		Outputs: outputs,
	}); err != nil {
		r.log.Error("final update failed", "task", id, "err", err)
	}
}
