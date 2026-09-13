// Command orrery submits workflows to a server and reads back what happened.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const usage = `orrery — submit workflows to an Orrery server and read what happened

usage:
  orrery submit <workflow.yml> [--repo R] [--event E] [--ref REF] [--wait]
  orrery dispatch <repo> <workflow-file> [--ref REF] [--input k=v ...] [--wait]
  orrery runs [--limit N]
  orrery run <id>
  orrery rerun <run-id> [--failed] [--wait]
  orrery logs <job-id> [--attempt N]
  orrery stop <job-id>

env:
  ORRERY_API_TOKEN   the server's -api-token; every command below needs it
  ORRERY_SERVER   server URL (default http://127.0.0.1:8080)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	base := strings.TrimRight(env("ORRERY_SERVER", "http://127.0.0.1:8080"), "/")

	var err error
	switch os.Args[1] {
	case "submit":
		err = submit(base, os.Args[2:])
	case "dispatch":
		err = dispatch(base, os.Args[2:])
	case "runs":
		err = listRuns(base, os.Args[2:])
	case "run":
		err = showRun(base, os.Args[2:])
	case "rerun":
		err = rerun(base, os.Args[2:])
	case "logs":
		err = showLogs(base, os.Args[2:])
	case "stop":
		err = stopJob(base, os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func flagValue(args []string, name, def string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
		if after, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return after
		}
	}
	return def
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name {
			return true
		}
	}
	return false
}

func submit(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery submit <workflow.yml>")
	}
	path := args[0]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Default repo/ref/sha to the checkout we are submitting from. These become
	// the `github` context, and actions read it literally: actions/checkout
	// rejects anything that is not owner/repo, so a placeholder like "local"
	// breaks the very first step of a realistic workflow.
	repo, ref, sha := gitIdentity(filepath.Dir(path))
	body := map[string]string{
		"repo":          flagValue(args, "repo", repo),
		"workflow_file": path,
		"workflow":      string(data),
		"event":         flagValue(args, "event", "manual"),
		"ref":           flagValue(args, "ref", ref),
		"sha":           flagValue(args, "sha", sha),
		"actor":         env("USER", "cli"),
	}
	var out struct {
		RunID int64    `json:"run_id"`
		Jobs  []string `json:"jobs"`
	}
	if err := post(base+"/api/runs", body, &out); err != nil {
		return err
	}
	fmt.Printf("run %d queued — %d job(s): %s\n", out.RunID, len(out.Jobs), strings.Join(out.Jobs, ", "))
	if !hasFlag(args, "wait") {
		return nil
	}
	return waitForRun(base, out.RunID)
}

// rerun starts a finished run over without a new commit.
func rerun(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery rerun <run-id> [--failed]")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return fmt.Errorf("bad run id %q", args[0])
	}
	url := fmt.Sprintf("%s/api/runs/%d/rerun", base, id)
	if hasFlag(args, "failed") {
		url += "?failed_only=true"
	}
	var out struct {
		RunID int64 `json:"run_id"`
		Jobs  int   `json:"jobs"`
	}
	if err := post(url, map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Printf("run %d re-queued — %d job(s)\n", out.RunID, out.Jobs)
	if !hasFlag(args, "wait") {
		return nil
	}
	return waitForRun(base, out.RunID)
}

// dispatch starts a manual run of a workflow already in the repository, the
// way GitHub's "Run workflow" button does.
func dispatch(base string, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: orrery dispatch <repo> <workflow-file> [--ref REF] [--input k=v ...]")
	}
	inputs := map[string]string{}
	for i := 0; i < len(args); i++ {
		if args[i] != "--input" || i+1 >= len(args) {
			continue
		}
		k, v, ok := strings.Cut(args[i+1], "=")
		if !ok {
			return fmt.Errorf("--input wants k=v, got %q", args[i+1])
		}
		inputs[k] = v
	}
	body := map[string]any{
		"repo":          args[0],
		"workflow_file": args[1],
		"ref":           flagValue(args, "ref", ""),
		"actor":         env("USER", "cli"),
		"inputs":        inputs,
	}
	var out struct {
		RunID int64    `json:"run_id"`
		Jobs  []string `json:"jobs"`
	}
	if err := post(base+"/api/dispatch", body, &out); err != nil {
		return err
	}
	fmt.Printf("run %d queued — %d job(s): %s\n", out.RunID, len(out.Jobs), strings.Join(out.Jobs, ", "))
	if !hasFlag(args, "wait") {
		return nil
	}
	return waitForRun(base, out.RunID)
}

// gitIdentity reads owner/repo, ref and sha out of the git checkout at dir,
// falling back to the working directory when the workflow file sits outside a
// repository — a scratch workflow run against the repo you are standing in is
// a normal thing to want, and it beats submitting a placeholder repository that
// actions/checkout will fail on.
//
// Anything it still cannot determine falls back to a value that is at least the
// right shape, so a workflow submitted from nowhere in particular still runs.
func gitIdentity(dir string) (repo, ref, sha string) {
	if !isGitRepo(dir) {
		dir = "."
	}
	repo, ref, sha = "orrery/local", "refs/heads/main", ""
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if url := git("remote", "get-url", "origin"); url != "" {
		if name := repoFromRemote(url); name != "" {
			repo = name
		}
	}
	if branch := git("rev-parse", "--abbrev-ref", "HEAD"); branch != "" && branch != "HEAD" {
		ref = "refs/heads/" + branch
	}
	if head := git("rev-parse", "HEAD"); head != "" {
		sha = head
	}
	return repo, ref, sha
}

func isGitRepo(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// repoFromRemote turns any of git@host:owner/repo.git, https://host/owner/repo
// or ssh://host/owner/repo.git into owner/repo.
func repoFromRemote(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndex(url, ":"); i >= 0 && !strings.Contains(url[i+1:], "/") {
		// scp-style git@host:owner/repo has no slash after the colon only when
		// the path itself is bare; otherwise fall through to the slash split.
		url = url[i+1:]
	}
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if i := strings.LastIndexAny(owner, ":"); i >= 0 {
		owner = owner[i+1:]
	}
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

func waitForRun(base string, id int64) error {
	fmt.Println("waiting...")
	for {
		var sum runSummary
		if err := get(fmt.Sprintf("%s/api/runs/%d", base, id), &sum); err != nil {
			return err
		}
		if sum.Run.Status == "done" {
			printRun(&sum)
			if sum.Run.Result != "success" {
				os.Exit(1)
			}
			return nil
		}
		time.Sleep(time.Second)
	}
}

type runSummary struct {
	Run struct {
		ID           int64  `json:"ID"`
		Repo         string `json:"Repo"`
		WorkflowName string `json:"WorkflowName"`
		Event        string `json:"Event"`
		Status       string `json:"Status"`
		Result       string `json:"Result"`
		RunAttempt   int64  `json:"RunAttempt"`
	} `json:"Run"`
	Jobs []struct {
		ID              int64  `json:"ID"`
		Key             string `json:"Key"`
		Name            string `json:"Name"`
		Status          string `json:"Status"`
		Result          string `json:"Result"`
		TimeoutMinutes  int    `json:"TimeoutMinutes"`
		StopReason      string `json:"StopReason"`
		ForceTerminated bool   `json:"ForceTerminated"`
		CleanupRan      bool   `json:"CleanupRan"`
		Steps           []struct {
			Index     int        `json:"Index"`
			Name      string     `json:"Name"`
			Result    string     `json:"Result"`
			StartedAt *time.Time `json:"StartedAt"`
			StoppedAt *time.Time `json:"StoppedAt"`
			LogIndex  int64      `json:"LogIndex"`
			LogLength int64      `json:"LogLength"`
		} `json:"Steps"`
	} `json:"Jobs"`
}

func listRuns(base string, args []string) error {
	limit := flagValue(args, "limit", "20")
	var runs []struct {
		ID           int64  `json:"ID"`
		Repo         string `json:"Repo"`
		WorkflowName string `json:"WorkflowName"`
		Event        string `json:"Event"`
		Status       string `json:"Status"`
		Result       string `json:"Result"`
	}
	if err := get(base+"/api/runs?limit="+limit, &runs); err != nil {
		return err
	}
	if len(runs) == 0 {
		fmt.Println("no runs yet")
		return nil
	}
	fmt.Printf("%-6s %-12s %-26s %-10s %s\n", "RUN", "STATUS", "WORKFLOW", "EVENT", "RESULT")
	for _, r := range runs {
		fmt.Printf("%-6d %-12s %-26s %-10s %s\n", r.ID, r.Status, truncate(r.WorkflowName, 26), r.Event, r.Result)
	}
	return nil
}

func showRun(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery run <id>")
	}
	var sum runSummary
	if err := get(base+"/api/runs/"+args[0], &sum); err != nil {
		return err
	}
	printRun(&sum)
	return nil
}

func printRun(sum *runSummary) {
	attempt := ""
	if sum.Run.RunAttempt > 1 {
		attempt = fmt.Sprintf("  attempt %d", sum.Run.RunAttempt)
	}
	fmt.Printf("run %d  %s  [%s]  %s%s\n",
		sum.Run.ID, sum.Run.WorkflowName, sum.Run.Status, sum.Run.Result, attempt)
	fmt.Printf("%-8s %-18s %-10s %-10s %-8s %s\n", "JOB", "KEY", "STATUS", "RESULT", "TIMEOUT", "NOTE")
	for _, j := range sum.Jobs {
		note := j.StopReason
		if j.ForceTerminated {
			// Say plainly that whatever the job was holding was never released.
			note = fmt.Sprintf("force-terminated, cleanup_ran=%v", j.CleanupRan)
		}
		fmt.Printf("%-8d %-18s %-10s %-10s %-8s %s\n",
			j.ID, truncate(j.Key, 18), j.Status, j.Result, fmt.Sprintf("%dm", j.TimeoutMinutes), note)
		for _, st := range j.Steps {
			// The log range is the point of printing this: it turns "which
			// step failed" into "which lines to read".
			lines := "-"
			if st.LogLength > 0 {
				lines = fmt.Sprintf("%d-%d", st.LogIndex, st.LogIndex+st.LogLength-1)
			}
			fmt.Printf("  %-2d %-38s %-10s %-9s log %s\n",
				st.Index, truncate(stepName(st.Name, st.Index), 38), st.Result,
				stepDuration(st.StartedAt, st.StoppedAt), lines)
		}
	}
}

func stepName(name string, index int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("step %d", index)
}

func stepDuration(started, stopped *time.Time) string {
	if started == nil || stopped == nil {
		return ""
	}
	return stopped.Sub(*started).Round(time.Millisecond).String()
}

func showLogs(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery logs <job-id>")
	}
	url := base + "/api/jobs/" + args[0] + "/logs"
	if a := flagValue(args, "attempt", ""); a != "" {
		url += "?attempt=" + a
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if tok := apiToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized: set ORRERY_API_TOKEN")
	}
	_, err = io.Copy(os.Stdout, res.Body)
	return err
}

func stopJob(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery stop <job-id>")
	}
	var out map[string]any
	if err := post(base+"/api/jobs/"+args[0]+"/stop?by="+env("USER", "cli"), nil, &out); err != nil {
		return err
	}
	fmt.Printf("stop requested for job %s — the runner acknowledges when it has wound down\n", args[0])
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func post(url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(req, out)
}

func get(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return do(req, out)
}

// apiToken is what the server's -api-token guards everything with. Read from
// the environment rather than a flag so it does not end up in shell history or
// in the process list next to a `ps`.
func apiToken() string { return os.Getenv("ORRERY_API_TOKEN") }

func do(req *http.Request, out any) error {
	if tok := apiToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error       string   `json:"error"`
			Unsupported []string `json:"unsupported"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			msg := e.Error
			for _, u := range e.Unsupported {
				msg += "\n  - " + u
			}
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
