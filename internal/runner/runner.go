package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// Options configure a runner.
type Options struct {
	Name   string
	Labels []string
	// WorkDir is where job working directories are created.
	WorkDir string
	// HeartbeatInterval is how often a running task reports in. The reply to
	// the heartbeat is also how a stop request reaches us, so this doubles as
	// the upper bound on how long we keep working after someone asks us not to.
	HeartbeatInterval time.Duration
	// LogFlushInterval bounds how long a line waits before shipping.
	LogFlushInterval time.Duration
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
}

// Version is what this runner advertises.
const Version = "0.1.0-p0a"

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
}

// New builds a runner.
func New(cl *Client, opts Options, log *slog.Logger) *Runner {
	opts.withDefaults()
	return &Runner{cl: cl, opts: opts, log: log}
}

// Run loops until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	if _, err := r.cl.Declare(ctx, &protocol.DeclareRequest{
		Version: Version, Labels: r.opts.Labels, Capabilities: Capabilities,
	}); err != nil {
		return fmt.Errorf("declare: %w", err)
	}
	r.log.Info("runner ready", "name", r.opts.Name, "labels", r.opts.Labels, "capabilities", Capabilities)

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
	jobKey, job, err := workflow.JobFromPayload(task.WorkflowPayload)
	if err != nil {
		r.finish(ctx, task.ID, protocol.ResultFailure, nil, nil)
		return err
	}
	r.log.Info("task started", "task", task.ID, "job", jobKey, "steps", len(job.Steps))

	dir := filepath.Join(r.opts.WorkDir, fmt.Sprintf("task-%d", task.ID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.finish(ctx, task.ID, protocol.ResultFailure, nil, nil)
		return err
	}
	defer os.RemoveAll(dir)

	logs := newLogShipper(r.cl, task.ID, r.opts.LogFlushInterval, r.log)
	defer logs.close(ctx)

	// stopCtx is what the steps run under. The heartbeat cancels it when the
	// server says stop; cleanup below deliberately does NOT run under it, so a
	// stop cannot interrupt the winding-down it just asked for.
	stopCtx, cancelSteps := context.WithCancel(ctx)
	defer cancelSteps()

	timeout := time.Duration(task.TimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = time.Duration(workflow.DefaultTimeoutMinutes) * time.Minute
	}
	stopCtx, cancelTimeout := context.WithTimeout(stopCtx, timeout)
	defer cancelTimeout()

	var (
		mu      sync.Mutex
		steps   []protocol.StepState
		stopped bool
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
			mu.Lock()
			snapshot := append([]protocol.StepState(nil), steps...)
			mu.Unlock()
			res, err := r.cl.UpdateTask(ctx, &protocol.UpdateTaskRequest{
				State: &protocol.TaskState{ID: task.ID, Steps: snapshot},
			})
			if err != nil {
				continue
			}
			if res.StopRequested {
				mu.Lock()
				already := stopped
				stopped = true
				mu.Unlock()
				if !already {
					r.log.Info("stop requested by server, winding down", "task", task.ID)
					logs.write("::orrery:: stop requested — winding down")
					cancelSteps()
				}
			}
		}
	}()

	result := protocol.ResultSuccess
	env := os.Environ()
	for k, v := range job.Env {
		env = append(env, k+"="+v)
	}

	for i, step := range job.Steps {
		started := time.Now().UTC()
		logIndex := logs.nextIndex()
		state := protocol.StepState{ID: int64(i), StartedAt: &started, LogIndex: logIndex}

		logs.write(fmt.Sprintf("::group::%s", step.Label(i)))
		runErr := r.runStep(stopCtx, dir, env, step, logs)
		logs.write("::endgroup::")

		stoppedAt := time.Now().UTC()
		state.StoppedAt = &stoppedAt
		state.LogLength = logs.nextIndex() - logIndex

		switch {
		case runErr == nil:
			state.Result = protocol.ResultSuccess
		case stopCtx.Err() != nil:
			state.Result = protocol.ResultCancelled
		case step.ContinueOnError:
			state.Result = protocol.ResultFailure
			logs.write(fmt.Sprintf("::warning::step failed but continue-on-error is set: %v", runErr))
		default:
			state.Result = protocol.ResultFailure
		}

		mu.Lock()
		steps = append(steps, state)
		mu.Unlock()

		if state.Result == protocol.ResultCancelled {
			result = protocol.ResultCancelled
			break
		}
		if state.Result == protocol.ResultFailure && !step.ContinueOnError {
			result = protocol.ResultFailure
			logs.write(fmt.Sprintf("::error::%s failed: %v", step.Label(i), runErr))
			break
		}
	}

	stopHeartbeat()

	mu.Lock()
	wasStopped := stopped
	final := append([]protocol.StepState(nil), steps...)
	mu.Unlock()

	// Cleanup runs under the parent context, never under stopCtx: the whole
	// point of asking a runner to stop is that it gets to finish tidying up.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancelCleanup()
	logs.flush(cleanupCtx)

	var ackedAt *time.Time
	if wasStopped {
		result = protocol.ResultCancelled
		now := time.Now().UTC()
		ackedAt = &now
		logs.write("::orrery:: cleanup complete, acknowledging stop")
		logs.flush(cleanupCtx)
	}

	r.log.Info("task complete", "task", task.ID, "result", result, "stopped", wasStopped)
	r.finish(cleanupCtx, task.ID, result, final, ackedAt)
	return nil
}

func (r *Runner) finish(ctx context.Context, id int64, result protocol.Result, steps []protocol.StepState, acked *time.Time) {
	now := time.Now().UTC()
	if _, err := r.cl.UpdateTask(ctx, &protocol.UpdateTaskRequest{
		State: &protocol.TaskState{
			ID: id, Result: result, StoppedAt: &now, StopAckedAt: acked, Steps: steps,
		},
	}); err != nil {
		r.log.Error("final update failed", "task", id, "err", err)
	}
}

// runStep executes a single `run:` step, streaming stdout and stderr into the
// log shipper as they arrive.
func (r *Runner) runStep(ctx context.Context, dir string, env []string, step workflow.Step, logs *logShipper) error {
	if step.Uses != "" {
		return fmt.Errorf("`uses:` is not executable in this build (%s)", step.Uses)
	}
	if step.Run == "" {
		return nil
	}
	shell := step.Shell
	if shell == "" {
		shell = "bash"
	}
	var cmd *exec.Cmd
	switch shell {
	case "bash", "sh":
		cmd = exec.CommandContext(ctx, shell, "-eo", "pipefail", "-c", step.Run)
	default:
		cmd = exec.CommandContext(ctx, shell, "-c", step.Run)
	}
	cmd.Dir = dir
	if step.WorkingDirectory != "" {
		cmd.Dir = filepath.Join(dir, step.WorkingDirectory)
		if err := os.MkdirAll(cmd.Dir, 0o755); err != nil {
			return err
		}
	}
	cmd.Env = append(append([]string(nil), env...), flatten(step.Env)...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		logs.write(scanner.Text())
	}
	return cmd.Wait()
}

func flatten(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}
