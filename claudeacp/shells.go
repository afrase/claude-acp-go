package claudeacp

import (
	"cmp"
	"encoding/json"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// Background commands and workflows. A Bash call that leaves its command
// running in the background completes at once (its result names the task
// and where the output goes), but the command is a task of claude's,
// which starts then and ends later: by itself, by claude's TaskStop, or
// killed from outside. A Workflow call is the same: it completes as the
// run launches, and the run is a task that ends later. Each change of
// the task goes to the client as a tool_call_update of the call that
// started it, with the task under _meta.claudeCode.task, every client
// alike; the call's own status stays as it was. claude-agent-acp sends
// these tasks only to JetBrains AIR, as async_task updates of its own;
// an update of the call keeps to what every client already reads.
//
// A workflow's agents are no tasks of their own: claude tells of them
// only in the run's task_progress, which isn't sent on.

// shellTypes are claude's task types for a background command and a
// workflow's run, and the tool that starts each.
var shellTypes = map[string]string{"local_bash": "Bash", "local_workflow": "Workflow"}

// shellTask is _meta.claudeCode.task: a background command's task as
// claude last told of it.
type shellTask struct {
	ID   string `json:"id"`   // the task's id: the call's toolResponse.backgroundTaskId
	Type string `json:"type"` // claude's task type: "local_bash", "local_workflow"
	// Status is "running", then claude's word for the end: "completed",
	// "failed" (killed from outside too), or "killed" then "stopped" for
	// a stop (claude's TaskStop, or stop_task: StopTaskMethod).
	Status     string `json:"status"`
	OutputFile string `json:"outputFile,omitempty"` // where claude wrote its output, once it ended
	// Summary is claude's line on the end: a command's that ended by
	// itself names its exit code; a stopped task's is its description.
	Summary string `json:"summary,omitempty"`
	EndTime int64  `json:"endTime,omitempty"` // when it ended, in milliseconds since the epoch
}

// shell is a background command's or a workflow's task, and the call
// that started it: its id, tool and stream ("" at the top, else a
// subagent's Agent call), and the task's type.
type shell struct {
	call, name, parent, typ string
	task                    shellTask // as the client last had it
}

// shellStarted is claude starting a task: a background command's or a
// workflow's runs.
func (ts *tools) shellStarted(m *claudeagent.TaskStarted) {
	tool, ok := shellTypes[m.TaskType]
	if !ok || m.TaskID == "" || m.ToolUseID == "" {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	sh := ts.shells[m.TaskID]
	if sh == nil {
		sh = &shell{call: m.ToolUseID, name: tool, typ: m.TaskType}
		if t := ts.byID[m.ToolUseID]; t != nil {
			sh.name, sh.parent = t.name, t.parent
		}
		ts.shells[m.TaskID] = sh
	}
	ts.shellLocked(sh, shellTask{ID: m.TaskID, Type: sh.typ, Status: "running"})
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

// A load replays a background command's or a workflow's end where the
// history has it: claude's notification of it, or claude's TaskStop of
// it (a command's gets no notification). The history never says a task
// still runs, so its start replays only when the live session runs it:
// then its task is as the live session has it.

// shellStarted is a call's result, for a background command or a
// workflow it started: a command's task's id is the result's
// backgroundTaskId, a workflow's its taskId, with taskType
// "local_workflow".
func (r *replayer) shellStarted(t *tool, b claudeagent.Block, structured json.RawMessage) {
	var res struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
		TaskID           string `json:"taskId"`
		TaskType         string `json:"taskType"`
	}
	if b.IsError || json.Unmarshal(structured, &res) != nil {
		return
	}
	id, typ := res.BackgroundTaskID, "local_bash"
	if res.TaskType == "local_workflow" {
		id, typ = res.TaskID, res.TaskType
	}
	if id == "" {
		return
	}
	sh := &shell{call: t.id, name: t.name, parent: t.parent, typ: typ}
	r.shells[id] = sh
	if task, ok := r.s.tools.shellNow(id); ok {
		r.s.tools.shell(sh, task)
	}
}

// shellEnded replays the end e of a background command or workflow the
// history started, once. A stop's notification says "killed" (claude
// writes it so for stop_task, whose stop the history keeps no TaskStop
// of): the live session's last word for a stop is "stopped", and so is
// the replay's.
func (r *replayer) shellEnded(e taskNote) {
	sh := r.shells[e.id]
	if sh == nil || e.status == "" || sh.task.Status != "" && sh.task.Status != "running" {
		return
	}
	status := e.status
	if status == "killed" {
		status = "stopped"
	}
	r.s.tools.shell(sh, shellTask{ID: e.id, Type: sh.typ, Status: status, OutputFile: e.outputFile, Summary: e.summary})
}

// shellStopped replays claude's TaskStop of the task id, if a background
// command's or a workflow's: it stopped.
func (r *replayer) shellStopped(id string) {
	r.shellEnded(taskNote{id: id, status: "stopped"})
}
