package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"
	actrunner "gitea.com/gitea/runner/act/runner"
	"github.com/sirupsen/logrus"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
)

// actExecutor runs a task through act, which is where `uses:` steps, container
// jobs and the expression engine come from.
//
// We depend on gitea.com/gitea/runner/act rather than nektos/act: nektos' master
// has had no commits since 2026-06-01 and its `gitea/act` fork is archived, with
// the code now living inside gitea/runner, which ships releases weekly.
type actExecutor struct {
	labels Labels
	// actionsURL is where `uses: owner/repo@ref` is resolved from. Today this
	// points at github.com; pointing it at a mirror is the whole of OQ-4 and
	// changes nothing else in this file.
	actionsURL string
	// offline refuses network fetches, serving only already-cached actions.
	offline  bool
	cacheDir string
	// dockerHost is where container jobs run. act defaults to
	// /var/run/docker.sock, which is wrong on every VM-backed setup.
	dockerHost string
	// mountDaemonSocket bind-mounts that daemon socket into the job container.
	// Off by default and deliberately so: a step with the daemon socket can
	// start a privileged container on the host, which is an escape out of the
	// sandbox the job container exists to be. GitHub-hosted runners do not do
	// it either. Turn it on only for a runner whose jobs are trusted.
	mountDaemonSocket bool
}

// execResult is what one act run reports back.
type execResult struct {
	Result  protocol.Result
	Steps   []protocol.StepState
	Outputs map[string]string
}

// run executes one job and streams its output into logs.
//
// ctx carries the stop: cancelling it is how a stop request or a timeout
// reaches the containers act started.
func (e *actExecutor) run(ctx context.Context, task *protocol.Task, workdir string, logs *logShipper) (*execResult, error) {
	wf, err := model.ReadWorkflow(strings.NewReader(string(task.WorkflowPayload)))
	if err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}
	jobID, job := firstJob(wf)
	if job == nil {
		return nil, fmt.Errorf("workflow payload carries no job")
	}
	plan, err := model.CombineWorkflowPlanner(wf).PlanJob(jobID)
	if err != nil {
		return nil, fmt.Errorf("plan job %s: %w", jobID, err)
	}

	gh := githubContext(task, jobID, job.Name)
	eventJSON, err := json.Marshal(gh.Event)
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}

	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(e.cacheDir, 0o755); err != nil {
		return nil, err
	}

	hook := newActHook(logs, len(job.Steps))

	cfg := &actrunner.Config{
		Workdir:        workdir,
		BindWorkdir:    false,
		ActionCacheDir: e.cacheDir,
		// Offline mode is how a runner with no egress still executes actions
		// it has already seen. It is the half of OQ-4 that does not need a
		// mirror to be useful.
		ActionOfflineMode: e.offline,

		ReuseContainers: false,
		AutoRemove:      true,
		LogOutput:       true,
		JSONLogger:      false,
		// NoSkipCheckout keeps actions/checkout doing real work instead of
		// act's local-directory shortcut; a CI engine that silently skips the
		// checkout is lying about what it ran.
		NoSkipCheckout: true,

		Env:     task.Vars,
		Secrets: task.Secrets,
		Vars:    task.Vars,
		Token:   gh.Token,

		// GitHubInstance, not the preset, is what act turns into
		// GITHUB_SERVER_URL: with a preset set it overwrites the preset's
		// ServerURL from this field, and an empty one yields the bare string
		// "https://", which fails actions/checkout with "Invalid URL".
		//
		// Known gap: the same code hard-codes Gitea's API shape on top —
		// GITHUB_API_URL becomes <instance>/api/v1 and GITHUB_GRAPHQL_URL is
		// blanked — with no config lever to correct it. Steps that clone are
		// unaffected; a step that calls the REST API against github.com is.
		// Fixing it means owning act's tree instead of tracking it upstream,
		// which is a product decision, not a patch.
		GitHubInstance:        forgeHost(gh.ServerURL),
		PresetGitHubContext:   gh,
		EventJSON:             string(eventJSON),
		EventName:             gh.EventName,
		Actor:                 gh.Actor,
		DefaultActionInstance: e.actionsURL,
		PlatformPicker:        e.labels.PickPlatform,
		ContainerNamePrefix:   fmt.Sprintf("ORRERY-TASK-%d", task.ID),
		ContainerMaxLifetime:  lifetime(ctx, task),
		ContainerDaemonSocket: e.daemonSocketMount(),
		CleanWorkdir:          true,
	}

	rr, err := actrunner.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("new act runner: %w", err)
	}

	ctx = common.WithLoggerHook(ctx, hook)
	execErr := rr.NewPlanExecutor(plan)(ctx)

	res := hook.result()
	if res.Result == protocol.ResultUnspecified {
		switch {
		case ctx.Err() != nil:
			res.Result = protocol.ResultCancelled
		case execErr != nil:
			res.Result = protocol.ResultFailure
		default:
			res.Result = protocol.ResultSuccess
		}
	}
	res.Outputs = job.Outputs
	// Hand execErr back rather than folding it into the result. act reports a
	// failed *start* only through this return value — nothing logs it — so
	// swallowing it here is what produces a failed job with an empty log.
	return res, execErr
}

// daemonSocketMount is what act should bind into the job container. "-" is
// act's sentinel for "mount nothing", which is our default; see
// mountDaemonSocket for why.
func (e *actExecutor) daemonSocketMount() string {
	if !e.mountDaemonSocket {
		return "-"
	}
	return e.dockerHost
}

// lifetime bounds how long act may keep containers alive. The server always
// sends a timeout, so the fallback here only matters for a malformed task.
func lifetime(ctx context.Context, task *protocol.Task) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	if task.TimeoutMinutes > 0 {
		return time.Duration(task.TimeoutMinutes) * time.Minute
	}
	return time.Hour
}

func firstJob(wf *model.Workflow) (string, *model.Job) {
	for _, id := range wf.GetJobIDs() {
		if j := wf.GetJob(id); j != nil {
			return id, j
		}
	}
	return "", nil
}

// githubContext builds the `github` context act hands to steps. Without it
// actions/checkout has no repository, no ref and no credential, which is why a
// task that carries only YAML cannot run a real workflow.
func githubContext(task *protocol.Task, jobID, jobName string) *model.GithubContext {
	get := func(key string) string {
		if task.Context == nil {
			return ""
		}
		v, _ := task.Context[key].(string)
		return v
	}
	repo := get("repository")
	ref := get("ref")
	gh := &model.GithubContext{
		Repository:      repo,
		RepositoryOwner: owner(repo),
		Ref:             ref,
		RefName:         strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/"),
		RefType:         refType(ref),
		Sha:             get("sha"),
		Actor:           get("actor"),
		EventName:       orElse(get("event_name"), "push"),
		Token:           get("token"),
		Workflow:        get("workflow"),
		RunID:           strconv.FormatInt(task.ID, 10),
		RunNumber:       strconv.FormatInt(task.ID, 10),
		Job:             jobID,
		JobName:         jobName,
		RetentionDays:   "0",
		Event:           map[string]any{},

		// actions/checkout builds its clone URL from ServerURL; an empty one
		// fails the step with a bare "Invalid URL" and no hint of the cause.
		ServerURL:  orElse(get("server_url"), "https://github.com"),
		APIURL:     orElse(get("api_url"), "https://api.github.com"),
		GraphQLURL: orElse(get("graphql_url"), "https://api.github.com/graphql"),
	}
	if raw, ok := task.Context["event"].(map[string]any); ok {
		gh.Event = raw
	}
	return gh
}

// forgeHost is what act wants in GitHubInstance: a host, or a full URL when the
// scheme is not https (an internal forge on http, say).
func forgeHost(serverURL string) string {
	if rest, ok := strings.CutPrefix(serverURL, "https://"); ok {
		return rest
	}
	if serverURL == "" {
		return "github.com"
	}
	return serverURL
}

func owner(repo string) string {
	if i := strings.IndexByte(repo, '/'); i > 0 {
		return repo[:i]
	}
	return ""
}

func refType(ref string) string {
	switch {
	case strings.HasPrefix(ref, "refs/tags/"):
		return "tag"
	case ref == "":
		return ""
	default:
		return "branch"
	}
}

func orElse(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ---------------------------------------------------------------- log hook --

// actHook turns act's logrus stream into our log lines and step states.
//
// act reports structurally: `raw_output` marks a line produced by the job
// itself, `stepNumber`/`step`/`stage`/`stepResult` mark a step moving, and
// `jobResult` marks the job settling. Everything else is act narrating, which
// we keep but mark so a reader can tell the engine's voice from the job's.
//
// `stage` matters more than it looks. act runs every step three times — Pre,
// Main, Post — and tags each pass with the same stepNumber. Timing a step from
// whichever pass logged first reports the checkout as starting before the step
// ahead of it, because its Pre pass ran in the job's setup. So only the Main
// pass defines a step's clock and log range, and a Post failure belongs to the
// job rather than to a step that already finished.
type actHook struct {
	logs *logShipper

	mu      sync.Mutex
	steps   []protocol.StepState
	jobRes  protocol.Result
	started map[int]bool
}

func newActHook(logs *logShipper, stepCount int) *actHook {
	return &actHook{
		logs:    logs,
		steps:   make([]protocol.StepState, 0, stepCount),
		started: map[int]bool{},
	}
}

func (h *actHook) Levels() []logrus.Level { return logrus.AllLevels }

// act's stage names.
const (
	stageMain = "Main"
	stagePost = "Post"
)

func (h *actHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if n, ok := intField(entry.Data, "stepNumber"); ok {
		stage, _ := entry.Data["stage"].(string)
		name, _ := entry.Data["step"].(string)
		h.ensureStep(n, name)
		if stage == stageMain {
			h.startStep(n, entry.Time)
		}
		if r, ok := entry.Data["stepResult"]; ok && stage != stagePost {
			h.settleStep(n, parseResult(r), entry.Time)
		}
	}
	if r, ok := entry.Data["jobResult"]; ok {
		if res := parseResult(r); res != protocol.ResultUnspecified {
			h.jobRes = res
		}
	}

	msg := strings.TrimRight(entry.Message, "\r\n")
	if msg == "" {
		return nil
	}
	if raw, _ := entry.Data["raw_output"].(bool); raw {
		h.logs.write(msg)
		return nil
	}
	// Engine narration, tagged so it is distinguishable from job output.
	if entry.Level <= logrus.WarnLevel {
		h.logs.write("::orrery:: " + msg)
	}
	return nil
}

func (h *actHook) ensureStep(n int, name string) {
	for len(h.steps) <= n {
		h.steps = append(h.steps, protocol.StepState{ID: int64(len(h.steps))})
	}
	if name != "" {
		h.steps[n].Name = name
	}
}

// startStep opens a step's clock and log range. Called on every Main-stage
// entry; only the first one counts.
func (h *actHook) startStep(n int, at time.Time) {
	if h.started[n] {
		return
	}
	h.started[n] = true
	t := at.UTC()
	h.steps[n].StartedAt = &t
	h.steps[n].LogIndex = h.logs.nextIndex()
}

func (h *actHook) settleStep(n int, res protocol.Result, at time.Time) {
	if n >= len(h.steps) || res == protocol.ResultUnspecified {
		return
	}
	if h.steps[n].Result != protocol.ResultUnspecified {
		return
	}
	// A step that never reached Main — a failed or skipped Pre — still needs a
	// start, or it settles with a stop time and no beginning.
	h.startStep(n, at)
	t := at.UTC()
	h.steps[n].Result = res
	h.steps[n].StoppedAt = &t
	h.steps[n].LogLength = h.logs.nextIndex() - h.steps[n].LogIndex
}

func (h *actHook) result() *execResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	return &execResult{
		Result: h.jobRes,
		Steps:  append([]protocol.StepState(nil), h.steps...),
	}
}

func intField(data logrus.Fields, key string) (int, bool) {
	switch v := data[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case uint64:
		return int(v), true
	default:
		return 0, false
	}
}

func parseResult(v any) protocol.Result {
	s, ok := v.(string)
	if !ok {
		if stringer, isStringer := v.(fmt.Stringer); isStringer {
			s = stringer.String()
		} else {
			s = fmt.Sprint(v)
		}
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success":
		return protocol.ResultSuccess
	case "failure":
		return protocol.ResultFailure
	case "cancelled", "canceled":
		return protocol.ResultCancelled
	case "skipped":
		return protocol.ResultSkipped
	default:
		return protocol.ResultUnspecified
	}
}

// defaultCacheDir is where fetched actions are kept between jobs.
func defaultCacheDir(workDir string) string {
	return filepath.Join(workDir, "action-cache")
}
