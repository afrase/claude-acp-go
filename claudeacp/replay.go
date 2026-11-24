package claudeacp

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// A loaded session's history, replayed to the client from claude's
// transcript as claude-agent-acp replays it, so a client that kept its
// own copy can line the two up: the
// user's messages under claude's uuids, the model's text under the API's
// message ids, each tool call as a tool_call with its whole input then
// its result (or failed, when the history has none); a prompt claude took
// into a running turn, kept only as an attachment, replays as the user's
// message where it was taken, as the SDK reads it. Usage, notices and
// permission requests aren't replayed. To
// a client of subagents, each subagent is announced after the call that
// started it, its own transcript replays on its session, and once the
// whole history is out each says how it ended. A background command's
// end replays where the history has it (shells.go).

// replayer is one replay of a session's history.
type replayer struct {
	s       *session
	t       *sessions.Transcript         // the session's transcript; nil when it has none
	agents  map[string]sessions.Subagent // subagents' transcripts, by the call that started each
	calls   map[callKey]*tool            // calls shown, until their results
	inputs  map[string]launch            // what the calls that start, message or stop a subagent said
	kids    []*replayedKid               // the subagents announced, in order
	byID    map[string]*replayedKid      // the same, by id
	byAgent map[string]sessions.Subagent // the same transcripts, by subagent
	queued  map[string][]queuedPrompt    // prompts claude kept other than as user messages, by the message before each
	said    map[string]bool              // the main transcript's messages' uuids
	sends   []sendCall                   // the SendMessage calls, in order
	// delivered are the SendMessage calls whose message a subagent's
	// transcript has, replayed there.
	delivered map[string]bool
	shells    map[string]*shell   // the background commands the history started, by task id
	launches  map[string]launched // the calls that started a subagent in the background, by id
}

// queuedPrompt is a prompt claude kept other than as a user message, and
// whether it is the main agent's: the user's, then, unless claude wrote
// it.
type queuedPrompt struct {
	sessions.Queued
	top bool
}

// callKey is a call of a replay by its stream and id: a stream's call
// never takes another's, should a transcript repeat an id.
type callKey struct{ stream, id string }

// replay sends the client the session's history, ms, found in cwd's
// history folder ("" for any).
func (s *session) replay(ms []sessions.Message, t *sessions.Transcript) {
	r := &replayer{s: s, t: t, agents: map[string]sessions.Subagent{}, calls: map[callKey]*tool{},
		inputs: map[string]launch{}, byID: map[string]*replayedKid{}, byAgent: map[string]sessions.Subagent{},
		queued: map[string][]queuedPrompt{}, said: map[string]bool{}, delivered: map[string]bool{}, shells: map[string]*shell{},
		launches: map[string]launched{}}
	for _, m := range ms {
		r.said[m.UUID] = true
	}
	if s.kids.children {
		r.findAgents(ms)
		r.findSends(ms)
	}
	r.findQueued(ms)
	r.messages(ms, "")
	r.unanswered()
	r.ends()
}

// noResultText is what a replayed call with no result in the history
// says: claude was stopped while it ran, or the session was forked
// before its result.
const noResultText = "The history has no result for this call."

// unanswered ends the calls the history shows with no result, as nothing
// will end them now; a call the live session still runs is left to it.
func (r *replayer) unanswered() {
	keys := slices.SortedFunc(maps.Keys(r.calls), func(a, b callKey) int {
		return cmp.Or(cmp.Compare(a.stream, b.stream), cmp.Compare(a.id, b.id))
	})
	for _, k := range keys {
		if t := r.calls[k]; !r.s.tools.running(t.id) {
			r.s.tools.show(t, report{status: acp.ToolCallStatusFailed, content: texts(noResultText)})
		}
	}
	clear(r.calls)
}

// running says the call id is one the session runs now.
func (ts *tools) running(id string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t := ts.byID[id]
	return t != nil && !t.ended
}

// messages replays ms, of the stream of the call stream ("" at the top).
func (r *replayer) messages(ms []sessions.Message, stream string) {
	r.notifiedAfter("")
	for _, m := range ms {
		var msg claudeagent.APIMessage
		if json.Unmarshal(m.Message, &msg) == nil {
			switch m.Type {
			case "assistant":
				r.assistant(stream, msg)
			case "user":
				r.user(stream, m, msg)
			}
		}
		r.notifiedAfter(m.UUID)
	}
}

func (r *replayer) assistant(stream string, msg claudeagent.APIMessage) {
	if stream == "" && signIn(&claudeagent.Assistant{Message: msg}) {
		return // claude's own "Please run /login", not the model's
	}
	for _, b := range msg.Content {
		switch {
		case b.Type == "text" && b.Text != "":
			r.chunk("agent_message_chunk", stream, msg.ID, acp.ContentBlock{Text: &acp.TextContent{Text: b.Text}})
		case b.Type == "thinking" && b.Thinking != "":
			r.chunk("agent_thought_chunk", stream, msg.ID, acp.ContentBlock{Text: &acp.TextContent{Text: b.Thinking}})
		case b.Type == "image":
			if img, ok := imageOf(b); ok {
				r.chunk("agent_message_chunk", stream, msg.ID, acp.ContentBlock{Image: img})
			}
		case isToolUse(b.Type):
			r.toolUse(stream, b)
		case strings.HasSuffix(b.Type, "_tool_result"):
			r.toolResult(stream, b, nil)
		}
	}
}

// user replays a user message: the user's words (only at the top: a
// subagent's are its prompt, which its first message carries) and tools'
// results. Tasks' ends it brings are kept for the subagents' ends.
func (r *replayer) user(stream string, m sessions.Message, msg claudeagent.APIMessage) {
	var structured json.RawMessage
	if len(msg.Content) == 1 { // else whose it is can't be told
		var rec struct {
			Result json.RawMessage `json:"toolUseResult"`
		}
		_ = json.Unmarshal(m.Raw, &rec)
		structured = rec.Result
	}
	if stream != "" {
		var text []string
		for _, b := range msg.Content {
			if b.Type == "text" {
				text = append(text, b.Text)
			}
		}
		r.delivery(stream, m, strings.Join(text, "\n"))
	}
	for _, b := range msg.Content {
		switch b.Type {
		case "tool_result":
			r.toolResult(stream, b, structured)
		case "text":
			r.notified(b.Text)
			if text, ok := sessions.UserText(b.Text); ok && stream == "" {
				r.chunk("user_message_chunk", "", m.UUID, acp.ContentBlock{Text: &acp.TextContent{Text: text}})
			}
		case "image":
			if img, ok := imageOf(b); ok && stream == "" {
				r.chunk("user_message_chunk", "", m.UUID, acp.ContentBlock{Image: img})
			}
		}
	}
}

// chunk sends a chunk of a message of the stream of call stream.
func (r *replayer) chunk(kind, stream, id string, c acp.ContentBlock) {
	r.s.kids.emit(stream, chunkUpdate(kind, stream, id, c))
}

// toolUse shows a call with its whole input; the main agent's plan tools
// make the plan instead, as live. A call that started a subagent is
// followed by the subagent's own history.
func (r *replayer) toolUse(stream string, b claudeagent.Block) {
	if isPlanTool(stream, b.Name) {
		r.s.plan.input(b)
		return
	}
	r.s.plan.watch(stream, b)
	t := &tool{id: b.ID, name: b.Name, parent: stream, whole: true}
	r.calls[callKey{stream, b.ID}] = t
	rep := wholeReport(t.name, b.Input, r.s.tools.cwd)
	var in launch
	if json.Unmarshal(rep.rawInput, &in) == nil {
		r.inputs[b.ID] = in
	}
	r.s.tools.show(t, rep)
	if a, ok := r.agents[b.ID]; ok {
		r.subagent(stream, b.ID, a, in)
	}
}

// toolResult ends a call shown of the stream of call stream, completed or
// failed, and keeps what it says of a subagent.
func (r *replayer) toolResult(stream string, b claudeagent.Block, structured json.RawMessage) {
	if r.s.plan.result(b, structured) {
		return
	}
	k := callKey{stream, b.ToolUseID}
	t := r.calls[k]
	if t == nil {
		return
	}
	delete(r.calls, k)
	r.s.tools.show(t, resultReport(t.name, b, structured))
	r.shellStarted(t, b, structured)
	r.launched(t, b, structured)
	r.called(t, b, structured)
}
