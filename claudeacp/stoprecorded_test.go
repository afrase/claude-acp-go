package claudeacp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// testdata/shells/stoptask and workflow-stoptask are claude's side of two
// recorded runs (Claude Code 2.1.331, a fake Messages API), trimmed as
// the others there: a background command, then a workflow, stopped by
// _session/stopTask with claude idle, a prompt after, and the stop sent
// again (and, for the command, for a task id claude never had). Their
// output files' folder is written /tmp/claude.

// A task stopped by _session/stopTask ends as claude's TaskStop ends it,
// on the call that started it: killed, then stopped, at once and with no
// turn of claude's; claude tells the model at the next prompt. claude
// answers a stop of a task already ended, or of one it never had, as it
// answers any: {}.
func TestStopTaskRecorded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prompts []string
		stops   []string // the stops, in order: one after the first prompt, the rest after the last
		want    []string
	}{
		{"stoptask", []string{"run it in the background", "is it still running"}, []string{"bfz31x1ks", "bfz31x1ks", "bnotatask1"}, []string{
			"call toolu_fake_0002 pending",
			shellUpdate(`{"id":"bfz31x1ks","type":"local_bash","status":"running"}`),
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			shellUpdate(`{"id":"bfz31x1ks","type":"local_bash","status":"killed","endTime":1795556325121}`),
			shellUpdate(`{"id":"bfz31x1ks","type":"local_bash","status":"stopped","outputFile":"/tmp/claude/tasks/bfz31x1ks.output","summary":"Wait long","endTime":1795556325121}`),
			"call toolu_fake_0006 pending",
			"update toolu_fake_0006 completed",
			"text",
			"usage human",
		}},
		{"workflow-stoptask", []string{"run the workflow", "is it done"}, []string{"wx4fxxffc", "wx4fxxffc"}, []string{
			"call toolu_fake_0002 pending",
			workflowUpdate(`{"id":"wx4fxxffc","type":"local_workflow","status":"running"}`),
			"update toolu_fake_0002 completed",
			"text",
			"usage human",
			workflowUpdate(`{"id":"wx4fxxffc","type":"local_workflow","status":"killed","endTime":1795556361267}`),
			workflowUpdate(`{"id":"wx4fxxffc","type":"local_workflow","status":"stopped","outputFile":"/tmp/claude/tasks/wx4fxxffc.output",` +
				`"summary":"Ask one helper that takes its time","endTime":1795556361267}`),
			"text",
			"usage human",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fk := withTitle(claudetest.Replay(readCassette(t, "shells", tc.name+".cli.jsonl")), `null`)
			p, sid := startOpen(t, nil, fk)
			prompt := func(text string) {
				t.Helper()
				select {
				case <-fk.Awaiting(`{"type":"user"}`):
				case <-time.After(guard):
					t.Fatalf("%q: claude's replay never wanted it: %v", text, fk.Err())
				}
				stopsWith(t, p.prompt(t, sid, text), "end_turn")
			}
			stop := func(id string) {
				t.Helper()
				select {
				case <-fk.Awaiting(`{"type":"control_request","request":{"subtype":"stop_task","task_id":"` + id + `"}}`):
				case <-time.After(guard):
					t.Fatalf("stop %s: claude's replay never wanted it: %v", id, fk.Err())
				}
				var r StopTaskResponse
				if err := p.Call(t.Context(), StopTaskMethod, StopTaskRequest{SessionID: string(sid), TaskID: id}, &r); err != nil {
					t.Fatalf("stop %s: %v", id, err)
				}
			}
			prompt(tc.prompts[0])
			stop(tc.stops[0])
			prompt(tc.prompts[1])
			for _, id := range tc.stops[1:] {
				stop(id)
			}
			<-fk.Played()
			if err := fk.Err(); err != nil {
				t.Fatal(err)
			}
			var got []string
			eventually(t, "the last update", func() bool {
				got = shellLines(t, p.notes())
				return len(got) >= len(tc.want) && got[len(got)-1] == tc.want[len(tc.want)-1]
			})
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// A load replays a stop by _session/stopTask as the live session ended
// it: stopped. claude's transcript keeps no TaskStop call for it, only
// its notification, queued for the model (a queue-operation, and, once a
// prompt took it, a queued_command attachment), whose status says
// "killed". Cut at the stop, the transcript is a session closed before
// another prompt, whose notification is still queued.
func TestLoadReplaysStopTaskEnds(t *testing.T) {
	for _, tc := range []struct{ name, sid, typ, want string }{
		{"stoptask", "f2b4b244-8e18-4abd-8a07-9ab6d724c492", "Bash",
			`{"id":"bfz31x1ks","type":"local_bash","status":"stopped","outputFile":"/tmp/claude/tasks/bfz31x1ks.output",` +
				`"summary":"Task \"Wait long\" was stopped by the user or by the app connected to this session"}`},
		{"workflow-stoptask", "55da9afc-edae-4b4c-aac9-70d548d6ea29", "Workflow",
			`{"id":"wx4fxxffc","type":"local_workflow","status":"stopped",` +
				`"summary":"Task \"Ask one helper that takes its time\" was stopped by the user or by the app connected to this session. No result will follow unless the user has it resumed."}`},
	} {
		b, err := os.ReadFile(filepath.Join("testdata", "shells", tc.name+".history.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitAfter(string(b), "\n")
		stop := slices.IndexFunc(lines, func(l string) bool {
			return strings.Contains(l, `"queue-operation"`) && strings.Contains(l, "<task-notification>")
		})
		if stop < 0 {
			t.Fatalf("%s: no queued notification", tc.name)
		}
		for _, cut := range []struct {
			name string
			text string
		}{{"whole", string(b)}, {"closed at the stop", strings.Join(lines[:stop+1], "")}} {
			t.Run(tc.name+"/"+cut.name, func(t *testing.T) {
				p := start(t, nil, fake())
				cwd := t.TempDir()
				dir := sessions.Store{ConfigDir: p.agent.cfg.ConfigDir}.ProjectDir(cwd)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.sid+".jsonl"), []byte(cut.text), 0o600); err != nil {
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
				sameLines(t, got, []string{keysSorted([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"toolu_fake_0002",` +
					`"_meta":{"claudeCode":{"toolName":"` + tc.typ + `","task":` + tc.want + `}}}`))})
			})
		}
	}
}
