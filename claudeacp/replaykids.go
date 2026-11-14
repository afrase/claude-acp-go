package claudeacp

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// Subagents in a replay. Each is announced on the session of the stream
// that started it, as live, its first message names that call, and its
// transcript replays on its session; the subagent is kept as the
// session's, so a SendMessage after the load runs it again on the same
// session. Its end goes once the whole history is out: what the last
// task notification about it said (a user message, or a prompt claude
// queued, in whichever transcript started it: findQueued); else stopped,
// when the main agent or the user (its sidecar says)
// stopped it; else, for one in the foreground, how its call ended (an
// error that names a stop is cancelled, as live); else
// idle with no reason, as nothing says (a background subagent that was
// running when claude last exited).

// replayedKid is a subagent the replay announced, and what the history
// says of its end.
type replayedKid struct {
	sa         *subagent
	call       string           // the call that started it
	live       *acp.StateUpdate // its state, when the session had it before this replay: that is its end
	background bool             // started in the background: its call's result says nothing of its end
	launched   string           // its first call's result: "ok", "cancelled", "failed", or "" with none
	failure    string           // that result's text, when it failed
	ended      string           // the status of the last task notification about it
	summary    string
	stopped    bool // the main agent (TaskStop) or the user stopped it
}

// findAgents finds the subagents' transcripts and the calls that started
// them: each one's sidecar names its call; else the call's result names
// the subagent (linkResults).
func (r *replayer) findAgents(ms []sessions.Message) {
	if r.t == nil {
		return
	}
	for _, a := range r.t.Subagents() {
		r.byAgent[a.ID] = a
		if a.ParentToolUseID != "" {
			r.agents[a.ParentToolUseID] = a
		}
	}
	r.linkResults(ms)
}

// linkResults finds the calls in ms, the main agent's or a subagent's
// messages, whose result names a subagent with no sidecar to name its
// call (an Agent call's toolUseResult.agentId).
func (r *replayer) linkResults(ms []sessions.Message) {
	for _, m := range ms {
		var rec struct {
			Message struct {
				Content claudeagent.Blocks `json:"content"`
			} `json:"message"`
			Result struct {
				AgentID string `json:"agentId"`
			} `json:"toolUseResult"`
		}
		if m.Type != "user" || json.Unmarshal(m.Raw, &rec) != nil || len(rec.Message.Content) != 1 {
			continue
		}
		call := rec.Message.Content[0].ToolUseID
		if a, ok := r.byAgent[rec.Result.AgentID]; ok && call != "" && a.ParentToolUseID == "" {
			r.agents[call] = a
		}
	}
}

// subagent announces the subagent a, started by call (with input in) in
// the stream of stream, then replays its transcript.
func (r *replayer) subagent(stream, call string, a sessions.Subagent, in launch) {
	if r.byID[a.ID] != nil {
		return // another call ran it again: the first one showed it
	}
	// claude may run it in the background unasked; its sidecar says so.
	in.Background = in.Background || a.Background
	sa, live := r.s.kids.replayed(a.ID, call, stream, in)
	kid := &replayedKid{sa: sa, call: call, live: live, background: in.Background, stopped: a.StoppedByUser}
	r.kids = append(r.kids, kid)
	r.byID[sa.id] = kid
	ms, err := a.Messages()
	if err != nil {
		r.s.log.Info("replaying a subagent", "agent", a.ID, "err", err)
		return
	}
	ms = stripFork(ms)
	r.linkResults(ms)
	r.findSends(ms)
	if qs, err := a.QueuedPrompts(); err == nil {
		r.place(qs, ms, false)
	}
	r.messages(ms, call)
}

// What claude writes before a fork child's directive, and the directive's
// start ("subagent_type": "fork").
const (
	forkOpen      = "<fork-boilerplate>"
	forkDirective = "</fork-boilerplate>\nYour directive: "
)

// stripFork is a subagent's transcript ms without what a fork child
// inherits, as claude-agent-acp's stripForkPrefix has it: a fork's starts
// with a copy of the message that launched it (that message's calls, the
// fork's own among them) and their "Fork started" results beside the
// directive message, all the parent's. It drops every message before the
// directive and keeps the directive's prompt alone. Other transcripts
// are as they were.
func stripFork(ms []sessions.Message) []sessions.Message {
	for i, m := range ms {
		var msg claudeagent.APIMessage
		if m.Type != "user" || json.Unmarshal(m.Message, &msg) != nil {
			continue
		}
		var prompt []claudeagent.Block
		directive := false
		for _, b := range msg.Content {
			if b.Type != "text" || !strings.HasPrefix(b.Text, forkOpen) {
				continue
			}
			directive = true
			if _, p, ok := strings.Cut(b.Text, forkDirective); ok {
				prompt = append(prompt, claudeagent.Block{Type: "text", Text: p})
			}
		}
		if !directive {
			continue
		}
		rest := ms[i+1:]
		if len(prompt) == 0 {
			return rest
		}
		body, err := json.Marshal(struct {
			Role    string              `json:"role"`
			Content []claudeagent.Block `json:"content"`
		}{"user", prompt})
		if err != nil {
			return rest
		}
		m.Message, m.Raw = body, nil
		return append([]sessions.Message{m}, rest...)
	}
	return ms
}

// called is a call's result, for what it says of a subagent: the end of
// the call that started one, a SendMessage that ran one again, or a
// TaskStop of one.
func (r *replayer) called(t *tool, b claudeagent.Block, structured json.RawMessage) {
	in := r.inputs[t.id]
	if kid := r.launchOf(t.id); kid != nil && kid.launched == "" {
		kid.launched = "ok"
		if b.IsError {
			kid.failure, _ = resultParts(b.Content)
			kid.launched = "failed"
			if stopWords.MatchString(kid.failure) {
				kid.launched = "cancelled"
			}
		}
		return
	}
	switch t.name {
	case "TaskStop":
		if kid := r.byID[in.TaskID]; kid != nil && !b.IsError {
			kid.stopped = true
		}
	case "SendMessage":
		var res struct {
			Success        bool   `json:"success"`
			ResumedAgentID string `json:"resumedAgentId"`
		}
		if json.Unmarshal(structured, &res) != nil || !res.Success {
			return
		}
		// Its message went where the subagent's transcript has it
		// (delivery), unless the transcript doesn't say.
		if kid := r.byID[res.ResumedAgentID]; kid != nil && !r.delivered[t.id] {
			r.rerun(kid, t.id, in.Message)
		}
	}
}

// stopWords are what the error result of a foreground subagent's call
// says when the user stopped it, as claude-agent-acp reads it: live,
// claude reports that end stopped, which isn't in the transcript.
var stopWords = regexp.MustCompile(`(?i)\b(?:cancelled|canceled|interrupted|stopped|killed)\b`)

// sendCall is a SendMessage call of the history: to whom, and its
// message.
type sendCall struct{ id, to, message string }

// findSends keeps the SendMessage calls in ms, a transcript, for the
// deliveries of their messages in the transcripts they went to.
func (r *replayer) findSends(ms []sessions.Message) {
	for _, m := range ms {
		var msg claudeagent.APIMessage
		if m.Type != "assistant" || json.Unmarshal(m.Message, &msg) != nil {
			continue
		}
		for _, b := range msg.Content {
			var in launch
			if isToolUse(b.Type) && b.Name == "SendMessage" && json.Unmarshal(b.Input, &in) == nil && in.To != "" && in.Message != "" {
				r.sends = append(r.sends, sendCall{b.ID, in.To, in.Message})
			}
		}
	}
}

// delivery replays a user message m, with text, of the subagent of the
// call stream if a SendMessage delivered it to the subagent (claude marks
// it origin.kind "coordinator"), as live and as claude-agent-acp replays
// it: the SendMessage's message to the subagent here, before the run it
// started. The call is the first not yet delivered to the subagent whose
// message the text has; with none, the text goes under m's uuid.
func (r *replayer) delivery(stream string, m sessions.Message, text string) {
	var rec struct {
		Origin struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}
	if strings.TrimSpace(text) == "" || json.Unmarshal(m.Raw, &rec) != nil || rec.Origin.Kind != "coordinator" {
		return
	}
	a, ok := r.agents[stream]
	kid := r.byID[a.ID]
	if !ok || kid == nil {
		return
	}
	names := []string{kid.sa.id, r.inputs[kid.call].Name}
	for _, c := range r.sends {
		if !r.delivered[c.id] && slices.Contains(names, c.to) && strings.Contains(text, strings.TrimSpace(c.message)) {
			r.delivered[c.id] = true
			r.rerun(kid, c.id, c.message)
			return
		}
	}
	r.s.kids.message(kid.sa, m.UUID, text)
}

// rerun is the subagent of kid run again by the SendMessage call with
// message. One the session had before this replay keeps the call it
// runs for now.
func (r *replayer) rerun(kid *replayedKid, call, message string) {
	if kid.live != nil {
		r.s.kids.message(kid.sa, call, message)
		return
	}
	r.s.kids.rerun(kid.sa, call, message)
}

// launchOf is the subagent call started, if the replay announced it.
func (r *replayer) launchOf(call string) *replayedKid {
	if a, ok := r.agents[call]; ok {
		if kid := r.byID[a.ID]; kid != nil && kid.call == call {
			return kid
		}
	}
	return nil
}

// findQueued reads the prompts claude kept other than as user messages
// (sessions.Store.QueuedPrompts): a prompt or a task notification
// delivered into a running turn is only an attachment, and one still
// queued when claude closed only a queue-operation, as recordings show.
// The end of a subagent started by a subagent goes to the one that
// started it, in that one's transcript (subagent). claude-agent-acp reads
// the attachments (the SDK makes them user messages) but not the
// queue-operations, so its replay leaves a subagent whose end was still
// queued idle with no reason.
func (r *replayer) findQueued(ms []sessions.Message) {
	if r.t == nil {
		return
	}
	qs, err := r.t.QueuedPrompts()
	if err != nil {
		return
	}
	r.place(qs, ms, true)
}

// place sets the prompts qs of a transcript whose messages are ms (the
// main agent's when top) to be read after the message before each, or
// first when that message isn't replayed; one still queued comes after
// them all.
func (r *replayer) place(qs []sessions.Queued, ms []sessions.Message, top bool) {
	known := map[string]bool{}
	for _, m := range ms {
		known[m.UUID] = true
	}
	for _, q := range qs {
		after := q.After
		if !known[after] {
			after = ""
		}
		r.queued[after] = append(r.queued[after], queuedPrompt{q, top})
	}
}

// notifiedAfter replays the prompts queued after the message uuid ("" for
// those first): what they say of subagents' ends is kept, and one the
// user sent while a turn ran, which claude took into it, is the user's
// message, under the uuid of the message it came from, unless that
// message is in the transcript too.
func (r *replayer) notifiedAfter(uuid string) {
	for _, p := range r.queued[uuid] {
		r.notified(p.Prompt)
		if !p.top || p.Pending || p.Meta || p.Notification || r.said[p.ID] {
			continue
		}
		if text, ok := userText(p.Prompt); ok {
			r.chunk("user_message_chunk", "", p.ID, acp.ContentBlock{Text: &acp.TextContent{Text: text}})
		}
	}
	delete(r.queued, uuid)
}

// notified keeps what the task notifications in a user message's text
// say of subagents' ends.
func (r *replayer) notified(text string) {
	for _, e := range taskEnds(text) {
		if kid := r.byID[e.id]; kid != nil {
			if _, ok := endState(e.status, ""); ok {
				kid.ended, kid.summary = e.status, e.summary
			}
		}
	}
}

// ends tells the client how each subagent announced ended.
func (r *replayer) ends() {
	ends := make([]replayEnd, 0, len(r.kids))
	for _, kid := range r.kids {
		if kid.live != nil {
			ends = append(ends, replayEnd{kid.sa, *kid.live, true})
			continue
		}
		st, ok := endState(kid.ended, kid.summary)
		switch {
		case ok:
		case kid.stopped:
			st, _ = endState("stopped", "")
		case !kid.background && kid.launched == "ok":
			st, _ = endState("completed", "")
		case !kid.background && kid.launched == "cancelled":
			st, _ = endState("stopped", "")
		case !kid.background && kid.launched == "failed":
			st, _ = endState("failed", kid.failure)
		default:
			st = acp.StateUpdate{Other: json.RawMessage(`{"state":"idle"}`)}
		}
		ends = append(ends, replayEnd{kid.sa, st, false})
	}
	r.s.kids.replayEnded(ends)
}

// replayed is the subagent id, started by call (of the stream named
// stream) with input in, as a replay announces it, with its first
// message. live is its state when the session had it already (it ran
// here, or an earlier load replayed it): it is announced again, for a
// client that rebuilds its view, but keeps what it runs for.
func (k *subagents) replayed(id, call, stream string, in launch) (sa *subagent, live *acp.StateUpdate) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if sa = k.byID[id]; sa != nil {
		st := sa.last
		if !sa.shown { // it ran here unseen: what is known of it
			st = acp.StateUpdate{Running: &acp.RunningStateUpdate{}}
			if sa.ended {
				st = acp.StateUpdate{Other: json.RawMessage(`{"state":"idle"}`)}
			}
		}
		live = &st
	} else {
		sa = &subagent{id: id, call: call, background: in.Background}
		k.byID[sa.id], k.byCall[call] = sa, sa
	}
	if !sa.shown {
		sa.on = k.sid
		if p := k.byCall[stream]; p != nil {
			sa.parent = cmp.Or(sa.parent, p.id)
			if p.shown {
				sa.on = p.id
			}
		}
	}
	k.announceLocked(sa, in, "", "")
	k.delegatedLocked(sa, call, in.Prompt)
	return sa, live
}

// message sends the subagent sa a message, id, that no call of the
// history names.
func (k *subagents) message(sa *subagent, id, text string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.delegatedLocked(sa, id, text)
}

// rerun is the subagent sa run again by the SendMessage call with
// message, in a replay.
func (k *subagents) rerun(sa *subagent, call, message string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	sa.call, k.byCall[call] = call, sa
	k.delegatedLocked(sa, call, message)
}

// replayEnd is how a subagent a replay announced ended: st, or, when
// live, its state now.
type replayEnd struct {
	sa   *subagent
	st   acp.StateUpdate
	live bool
}

// replayEnded tells the client how each subagent a replay announced
// ended, in order.
func (k *subagents) replayEnded(ends []replayEnd) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, e := range ends {
		if !e.live {
			e.sa.ended = true
			e.sa.endedRuns = map[string]bool{e.sa.call: true}
		}
		k.childStateLocked(e.sa, e.st)
	}
}
