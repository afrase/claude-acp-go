package claudeacp

import (
	"context"
	"encoding/json"
	"strings"

	acp "github.com/afrase/acp-go/unstable"
)

// StopTaskMethod is an extension of this agent's: it stops one of an open
// session's background tasks by its id, with claude's stop_task control
// request. No turn runs and no tokens are spent; the task's end then
// comes as claude tells it (for a background command or a workflow, on
// the call that started it: see the README's "Background commands"). It
// isn't part of ACP.
//
// It is for a task without a session of its own: a background command, a
// workflow. A subagent shown as a session of its own is better stopped by
// session/cancel on that session, which also withdraws what the subagent
// (and the foreground subagents it waits on) still asks the user; this
// method stops its task alone.
//
// In a recording of Claude Code 2.1.331, a stop of a background command
// and of a workflow, with claude idle, ended the task as claude's
// TaskStop does (task_updated "killed", then task_notification
// "stopped", before claude answered) and started no turn: claude tells
// the model of the stop at the next prompt. claude's answer is passed on
// as it comes, beside its messages, not after them: those updates may
// reach the client just after {}, so a client ends the task on them, not
// on the answer. claude answered a stop of a
// task already ended, and of a task id it never had, as it answers any:
// with success, so {} says claude took the request, not that a task was
// running. An error claude gives is the call's error, in claude's words.
const StopTaskMethod = "_session/stopTask"

// StopTaskRequest is StopTaskMethod's params: the open session, and the
// task's id (claude's task_id: a background command's backgroundTaskId,
// a workflow's taskId).
type StopTaskRequest struct {
	SessionID string `json:"sessionId"`
	TaskID    string `json:"taskId"`
}

// StopTaskResponse is its answer: empty, once claude has taken the stop.
type StopTaskResponse struct{}

// stopTask answers StopTaskMethod.
func (a *agent) stopTask(ctx context.Context, params json.RawMessage) (*StopTaskResponse, error) {
	var req StopTaskRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams("%s: %v", StopTaskMethod, err)
	}
	if strings.TrimSpace(req.TaskID) == "" {
		return nil, invalidParams("%s: no task id", StopTaskMethod)
	}
	s, err := a.session(acp.SessionID(req.SessionID))
	if err != nil {
		return nil, err
	}
	if err := s.cs.StopTask(ctx, req.TaskID); err != nil {
		return nil, err
	}
	return &StopTaskResponse{}, nil
}
