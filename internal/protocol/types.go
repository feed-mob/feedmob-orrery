// Package protocol implements the runner.v1 wire contract.
//
// The shapes below mirror gitea/actions-proto-def's runner/v1 messages so a
// runner written against either side can talk to the other. We hand-write the
// Go types instead of generating them: the whole contract is ~5KB of proto and
// carrying protoc into the build buys us nothing at this size.
//
// Transport is Connect's HTTP/JSON variant — POST /runner.v1.RunnerService/{Method}
// with a JSON body — so swapping in real gRPC later is a codec change, not a
// redesign.
package protocol

import "time"

// ServicePath is the Connect-style prefix every RPC hangs off.
const ServicePath = "/runner.v1.RunnerService/"

// Result mirrors the job/step conclusions in GitHub's jobs context.
type Result string

const (
	ResultUnspecified Result = ""
	ResultSuccess     Result = "success"
	ResultFailure     Result = "failure"
	ResultCancelled   Result = "cancelled"
	ResultSkipped     Result = "skipped"
)

// Done reports whether the result is terminal. An unspecified result means the
// task is still in flight.
func (r Result) Done() bool { return r != ResultUnspecified }

// RunnerStatus is the lifecycle of a registered runner.
type RunnerStatus string

const (
	RunnerStatusUnspecified RunnerStatus = ""
	RunnerStatusIdle        RunnerStatus = "idle"
	RunnerStatusActive      RunnerStatus = "active"
	RunnerStatusOffline     RunnerStatus = "offline"
)

// Capability flags a runner advertises at registration. The server uses these
// to decide how a task may be stopped: a runner that does not advertise
// CapabilityCancelling can only be force-terminated, and the run ledger records
// that its cleanup never ran.
const (
	CapabilityCancelling = "cancelling"
)

// Runner is the server's view of a registered runner.
type Runner struct {
	ID       int64        `json:"id"`
	UUID     string       `json:"uuid"`
	Token    string       `json:"token,omitempty"`
	Name     string       `json:"name"`
	Status   RunnerStatus `json:"status"`
	Version  string       `json:"version,omitempty"`
	Labels   []string     `json:"labels,omitempty"`
	Ephemral bool         `json:"ephemeral,omitempty"`
}

// ---- Register ----

type RegisterRequest struct {
	Name         string   `json:"name"`
	Token        string   `json:"token"`
	Version      string   `json:"version,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Ephemeral    bool     `json:"ephemeral,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type RegisterResponse struct {
	Runner *Runner `json:"runner"`
}

// ---- Declare ----

type DeclareRequest struct {
	Version      string   `json:"version,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type DeclareResponse struct {
	Runner *Runner `json:"runner"`
}

// ---- FetchTask ----

// FetchTaskRequest carries the tasks_version the runner last saw. The server
// bumps a global counter whenever work is queued; a runner whose version
// already matches can skip the dispatch query entirely.
type FetchTaskRequest struct {
	TasksVersion int64 `json:"tasks_version,omitempty"`
}

type FetchTaskResponse struct {
	Task         *Task `json:"task,omitempty"`
	TasksVersion int64 `json:"tasks_version"`
}

// TaskNeed is one entry of the needs context: what an upstream job produced and
// how it ended.
type TaskNeed struct {
	Outputs map[string]string `json:"outputs,omitempty"`
	Result  Result            `json:"result,omitempty"`
}

// Task is one dispatchable unit of work — a single job of a single run.
//
// WorkflowPayload is the expanded YAML for just this job, so the runner never
// has to re-resolve matrices or `needs` ordering; that already happened server
// side, which is where the ledger lives.
type Task struct {
	ID              int64               `json:"id"`
	WorkflowPayload []byte              `json:"workflow_payload,omitempty"`
	Context         map[string]any      `json:"context,omitempty"`
	Secrets         map[string]string   `json:"secrets,omitempty"`
	Needs           map[string]TaskNeed `json:"needs,omitempty"`
	Vars            map[string]string   `json:"vars,omitempty"`

	// TimeoutMinutes is Orrery's addition to the contract. GitHub Actions has
	// no org-level default and 12 of our 13 workflows shipped without one, so
	// the server always sends a value — the workflow may lower it, never omit
	// it.
	TimeoutMinutes int `json:"timeout_minutes,omitempty"`
}

// ---- UpdateTask ----

type StepState struct {
	ID        int64      `json:"id"`
	Result    Result     `json:"result,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	StoppedAt *time.Time `json:"stopped_at,omitempty"`
	LogIndex  int64      `json:"log_index,omitempty"`
	LogLength int64      `json:"log_length,omitempty"`
}

// TaskState is what the runner reports back about a task in flight.
//
// StopAckedAt is Orrery's addition: the proto has no way for a runner to say
// "I saw your stop and I have finished cleaning up". Without it a stop is a
// request nobody confirms, which is how two production conversations went
// permanently mute in Mobius. A task whose StopRequested is set but which never
// acks is force-terminated by the reaper and flagged as cleanup-not-run.
type TaskState struct {
	ID          int64       `json:"id"`
	Result      Result      `json:"result,omitempty"`
	StartedAt   *time.Time  `json:"started_at,omitempty"`
	StoppedAt   *time.Time  `json:"stopped_at,omitempty"`
	StopAckedAt *time.Time  `json:"stop_acked_at,omitempty"`
	Steps       []StepState `json:"steps,omitempty"`
}

type UpdateTaskRequest struct {
	State   *TaskState        `json:"state"`
	Outputs map[string]string `json:"outputs,omitempty"`
}

// UpdateTaskResponse echoes the server's view back. StopRequested going true is
// how a runner learns it should wind down: there is no server-to-runner channel,
// so the signal rides the reply to the runner's own heartbeat.
type UpdateTaskResponse struct {
	State         *TaskState `json:"state"`
	SentOutputs   []string   `json:"sent_outputs,omitempty"`
	StopRequested bool       `json:"stop_requested,omitempty"`
}

// ---- UpdateLog ----

type LogRow struct {
	Time    time.Time `json:"time"`
	Content string    `json:"content"`
}

// UpdateLogRequest submits a contiguous window of log lines starting at Index.
type UpdateLogRequest struct {
	TaskID int64    `json:"task_id"`
	Index  int64    `json:"index"`
	Rows   []LogRow `json:"rows,omitempty"`
	NoMore bool     `json:"no_more,omitempty"`
}

// UpdateLogResponse acknowledges up to AckIndex. Delivery is defined by this
// ack, not by the HTTP 200 — the runner trims its buffer to AckIndex and
// resends anything past it.
type UpdateLogResponse struct {
	AckIndex int64 `json:"ack_index"`
}
