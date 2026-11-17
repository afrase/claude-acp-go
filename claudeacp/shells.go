package claudeacp

import (
	"cmp"
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// Background commands. A Bash call that leaves its command running in
// the background completes at once (its result names the task and where
// the output goes), but the command is a task of claude's, which starts
// then and ends later: by itself, by claude's TaskStop, or killed from
// outside. Each change of the task goes to the client as a
// tool_call_update of the call that started it, with the task under
// _meta.claudeCode.task, every client alike; the call's own status stays
// as it was. claude-agent-acp sends these tasks only to JetBrains AIR,
// as async_task updates of its own; an update of the call keeps to what
// every client already reads.

// shellType is claude's task type for a background command.
const shellType = "local_bash"

// shellTask is _meta.claudeCode.task: a background command's task as
// claude last told of it.
type shellTask struct {
	ID   string `json:"id"`   // the task's id: the call's toolResponse.backgroundTaskId
	Type string `json:"type"` // claude's task type, "local_bash"
	// Status is "running", then claude's word for the end: "completed",
	// "failed" (killed from outside too), or "killed" then "stopped" for
	// a stop (claude's TaskStop).
	Status     string `json:"status"`
	OutputFile string `json:"outputFile,omitempty"` // where claude wrote its output, once it ended
	Summary    string `json:"summary,omitempty"`    // claude's line on the end, which names an exit code
	EndTime    int64  `json:"endTime,omitempty"`    // when it ended, in milliseconds since the epoch
}

// shell is a background command's task, and the call that started it:
// its id, tool and stream ("" at the top, else a subagent's Agent call).
type shell struct {
	call, name, parent string
	task               shellTask // as the client last had it
}

// shellStarted is claude starting a task: a background command's runs.
func (ts *tools) shellStarted(m *claudeagent.TaskStarted) {
	if m.TaskType != shellType || m.TaskID == "" || m.ToolUseID == "" {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	sh := ts.shells[m.TaskID]
	if sh == nil {
		sh = &shell{call: m.ToolUseID, name: "Bash"}
		if t := ts.byID[m.ToolUseID]; t != nil {
			sh.name, sh.parent = t.name, t.parent
		}
		ts.shells[m.TaskID] = sh
	}
	ts.shellLocked(sh, shellTask{ID: m.TaskID, Type: shellType, Status: "running"})
}

// shellUpdated is a change to a task: a background command's status, and
// when it ended.
func (ts *tools) shellUpdated(m *claudeagent.TaskUpdated) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	sh := ts.shells[m.TaskID]
	if sh == nil {
		return
	}
	task := sh.task
	task.Status = cmp.Or(m.Patch.Status, task.Status)
	task.EndTime = cmp.Or(m.Patch.EndTime, task.EndTime)
	ts.shellLocked(sh, task)
}

// shellEnded is claude's notification of a task's end: a background
// command's last word, with its output file and claude's summary. The
// task is forgotten then.
func (ts *tools) shellEnded(m *claudeagent.TaskNotification) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	sh := ts.shells[m.TaskID]
	if sh == nil {
		return
	}
	delete(ts.shells, m.TaskID)
	task := sh.task
	task.Status = cmp.Or(m.Status, task.Status)
	task.OutputFile = cmp.Or(m.OutputFile, task.OutputFile)
	task.Summary = cmp.Or(m.Summary, task.Summary)
	ts.shellLocked(sh, task)
}

// shellNow is the task id as the live session last told of it, while
// claude runs it or until its notification.
func (ts *tools) shellNow(id string) (shellTask, bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if sh := ts.shells[id]; sh != nil {
		return sh.task, true
	}
	return shellTask{}, false
}

// shell tells the client of task, as shellLocked.
func (ts *tools) shell(sh *shell, task shellTask) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.shellLocked(sh, task)
}

// shellLocked tells the client of sh's task as task, if it is news: an
// update of the call that started it, which says only _meta.
func (ts *tools) shellLocked(sh *shell, task shellTask) {
	if task == sh.task {
		return
	}
	sh.task = task
	meta := claudeCodeMeta{ToolName: sh.name, ParentToolUseID: sh.parent, Task: &task}.meta()
	ts.out.emit(sh.parent, acp.SessionUpdate{SessionUpdate: "tool_call_update", ToolCallUpdate: &acp.ToolCallUpdate{
		ToolCallID: acp.ToolCallID(sh.call), Meta: meta,
	}})
}

// A load replays a background command's end where the history has it:
// claude's notification of it, or claude's TaskStop of it, which gets no
// notification. The history never says a command still runs, so a
// command's start replays only when the live session runs it: then its
// task is as the live session has it.

// shellStarted is a call's result, for a background command it started:
// its task's id is the result's backgroundTaskId.
func (r *replayer) shellStarted(t *tool, b claudeagent.Block, structured json.RawMessage) {
	var res struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
	}
	if b.IsError || json.Unmarshal(structured, &res) != nil || res.BackgroundTaskID == "" {
		return
	}
	sh := &shell{call: t.id, name: t.name, parent: t.parent}
	r.shells[res.BackgroundTaskID] = sh
	if task, ok := r.s.tools.shellNow(res.BackgroundTaskID); ok {
		r.s.tools.shell(sh, task)
	}
}

// shellEnded replays the end e of a background command the history
// started, once.
func (r *replayer) shellEnded(e taskNote) {
	sh := r.shells[e.id]
	if sh == nil || e.status == "" || sh.task.Status != "" && sh.task.Status != "running" {
		return
	}
	r.s.tools.shell(sh, shellTask{ID: e.id, Type: shellType, Status: e.status, OutputFile: e.outputFile, Summary: e.summary})
}

// shellStopped replays claude's TaskStop of the task id, if a background
// command's: it stopped.
func (r *replayer) shellStopped(id string) {
	r.shellEnded(taskNote{id: id, status: "stopped"})
}
