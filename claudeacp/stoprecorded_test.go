package claudeacp

import (
	"strings"
	"testing"
	"time"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
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
