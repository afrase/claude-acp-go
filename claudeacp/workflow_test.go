package claudeacp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// testdata/shells/workflow and workflow-stop are claude's side of two
// recorded runs of a workflow (Claude Code 2.1.325, a fake Messages API),
// trimmed to what the agent reads (no hooks, no partial messages): one
// that completes, and one stopped by claude's TaskStop. Their
// .history.jsonl are the transcripts claude wrote, trimmed to the
// conversation's records. Their output files' folder is written
// /tmp/claude.

// workflowUpdate is the update of the Workflow call toolu_fake_0002 that
// carries task, as the client receives it.
func workflowUpdate(task string) string {
	return keysSorted([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"toolu_fake_0002","_meta":{"claudeCode":{"toolName":"Workflow","task":` + task + `}}}`))
}

// playShells plays the recording name to a client that takes no
// subagents, sending prompts in turn, and returns what the client got,
// as shellLines has it, once the last update is want's last.
func playShells(t *testing.T, name string, prompts, want []string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "shells", name+".cli.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := claudetest.ReadCassette(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	fk := withTitle(claudetest.Replay(c), `null`)
	p, sid := startOpen(t, nil, fk)
	for _, prompt := range prompts {
		select {
		case <-fk.Awaiting(`{"type":"user"}`):
		case <-time.After(guard):
			t.Fatalf("%q: claude's replay never wanted it: %v", prompt, fk.Err())
		}
		if got := stopReason(p.prompt(t, sid, prompt)); got != "end_turn" {
			t.Fatalf("%q: %s", prompt, got)
		}
	}
	<-fk.Played()
	if err := fk.Err(); err != nil {
		t.Fatal(err)
	}
	// The last update comes after the replay ends; what came instead
	// shows, if it never does.
	for deadline := time.Now().Add(guard); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if got := shellLines(t, p.notes()); len(got) >= len(want) && got[len(got)-1] == want[len(want)-1] {
			break
		}
	}
	return shellLines(t, p.notes())
}

// A workflow's task goes to the client as a background command's does, on
// the Workflow call that started it: running as claude starts it, then
// its end as claude tells it (task_updated, then task_notification), its
// type as claude sends it. Its progress, and its agents (which claude
// tells of only in that progress), send nothing.
func TestWorkflowTasks(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompts []string
		want    []string
	}{
		{"workflow", []string{"run the workflow"}, []string{
			"call toolu_fake_0002 pending",
			workflowUpdate(`{"id":"w9u1xb6ik","type":"local_workflow","status":"running"}`),
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			workflowUpdate(`{"id":"w9u1xb6ik","type":"local_workflow","status":"completed","endTime":1794798436156}`),
			workflowUpdate(`{"id":"w9u1xb6ik","type":"local_workflow","status":"completed","outputFile":"/tmp/claude/tasks/w9u1xb6ik.output",` +
				`"summary":"Dynamic workflow \"Ask four helpers in two phases\" completed","endTime":1794798436156}`),
			"text",
			"usage task-notification",
		}},
		{"workflow-stop", []string{"run the workflow", "stop it"}, []string{
			"call toolu_fake_0002 pending",
			workflowUpdate(`{"id":"w7wwz94yd","type":"local_workflow","status":"running"}`),
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			"call toolu_fake_0007 pending",
			"update toolu_fake_0007 completed",
			"call toolu_fake_0009 pending",
			workflowUpdate(`{"id":"w7wwz94yd","type":"local_workflow","status":"killed","endTime":1794798611121}`),
			workflowUpdate(`{"id":"w7wwz94yd","type":"local_workflow","status":"stopped","outputFile":"/tmp/claude/tasks/w7wwz94yd.output",` +
				`"summary":"Ask one helper that takes its time","endTime":1794798611121}`),
			"update toolu_fake_0009 completed",
			"text",
			"usage human",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := playShells(t, tc.name, tc.prompts, tc.want); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// A load replays a workflow's end where the history has it, on its
// Workflow call, as a background command's: claude's notification of it,
// or claude's TaskStop of it, which the history keeps no notification of.
func TestLoadReplaysWorkflowEnds(t *testing.T) {
	for _, tc := range []struct{ name, sid, want string }{
		{"workflow", "511e5af9-4263-4b34-8312-246b3236eca0",
			`{"id":"w9u1xb6ik","type":"local_workflow","status":"completed","outputFile":"/tmp/claude/tasks/w9u1xb6ik.output",` +
				`"summary":"Dynamic workflow \"Ask four helpers in two phases\" completed"}`},
		{"workflow-stop", "8461cc4f-b3ec-410f-8f76-96022e939872", `{"id":"w7wwz94yd","type":"local_workflow","status":"stopped"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := start(t, nil, fake())
			cwd := t.TempDir()
			b, err := os.ReadFile(filepath.Join("testdata", "shells", tc.name+".history.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			dir := sessions.Store{ConfigDir: p.agent.cfg.ConfigDir}.ProjectDir(cwd)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.sid+".jsonl"), b, 0o600); err != nil {
				t.Fatal(err)
			}
			p.mustLoad(t, tc.sid, cwd)
			var got []string
			for _, n := range p.notes() {
				if u := n.Update.ToolCallUpdate; u != nil && claudeMeta(u.Meta)["task"] != nil {
					b, _ := json.Marshal(n.Update)
					got = append(got, keysSorted(b))
				}
			}
			sameLines(t, got, []string{workflowUpdate(tc.want)})
		})
	}
}
