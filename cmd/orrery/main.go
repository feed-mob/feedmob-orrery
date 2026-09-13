// Command orrery submits workflows to a server and reads back what happened.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const usage = `orrery — submit workflows to an Orrery server and read what happened

usage:
  orrery submit <workflow.yml> [--repo R] [--event E] [--ref REF] [--wait]
  orrery runs [--limit N]
  orrery run <id>
  orrery logs <job-id>
  orrery stop <job-id>

env:
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
	case "runs":
		err = listRuns(base, os.Args[2:])
	case "run":
		err = showRun(base, os.Args[2:])
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
	body := map[string]string{
		"repo":          flagValue(args, "repo", "local"),
		"workflow_file": path,
		"workflow":      string(data),
		"event":         flagValue(args, "event", "manual"),
		"ref":           flagValue(args, "ref", ""),
		"sha":           flagValue(args, "sha", ""),
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
	fmt.Printf("run %d  %s  [%s]  %s\n", sum.Run.ID, sum.Run.WorkflowName, sum.Run.Status, sum.Run.Result)
	fmt.Printf("%-8s %-18s %-10s %-10s %-8s %s\n", "JOB", "KEY", "STATUS", "RESULT", "TIMEOUT", "NOTE")
	for _, j := range sum.Jobs {
		note := j.StopReason
		if j.ForceTerminated {
			// Say plainly that whatever the job was holding was never released.
			note = fmt.Sprintf("force-terminated, cleanup_ran=%v", j.CleanupRan)
		}
		fmt.Printf("%-8d %-18s %-10s %-10s %-8s %s\n",
			j.ID, truncate(j.Key, 18), j.Status, j.Result, fmt.Sprintf("%dm", j.TimeoutMinutes), note)
	}
}

func showLogs(base string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: orrery logs <job-id>")
	}
	res, err := http.Get(base + "/api/jobs/" + args[0] + "/logs")
	if err != nil {
		return err
	}
	defer res.Body.Close()
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

func do(req *http.Request, out any) error {
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
