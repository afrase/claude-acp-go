package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// cancel stops the session's work: session/cancel, the seq-th of the
// client's messages. Prompts claude hasn't taken are answered cancelled
// at once and dropped from claude's queue, with the steers that joined
// them, and so are the running turn's steers claude hasn't taken; claude
// is interrupted, and a running prompt answered cancelled at its result
// or idle, or after the backstop. A prompt from before seq that is still
// setting up is cancelled as it registers (send). Until a turn starts
// again, or claude asks the API anew, what the stopped work still writes
// at the top level is dropped; subagents' isn't, as background ones go
// on.
func (s *session) cancel(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelSeq = max(s.cancelSeq, seq)
	// Claude is interrupted even when no prompt is known to run: it may
	// have taken one whose echo hasn't come yet, or be at work of its own.
	s.stopLocked(slices.Clone(s.turns), true)
}

// withdrawTurn stops t alone, a prompt whose request the client withdrew:
// one claude hasn't taken is answered cancelled and dropped from its
// queue; the one it runs, or may have taken, is interrupted as a cancel
// would, but the prompts queued after it go on.
func (s *session) withdrawTurn(t *turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.settled {
		return
	}
	taken := t.active || (s.active == nil && len(s.turns) > 0 && s.turns[0] == t)
	s.stopLocked([]*turn{t}, taken)
}

// stopLocked answers ts cancelled, or has them answered so once claude
// has stopped them (the backstop bounds the wait), and writes claude the
// drops of the user messages it hasn't taken. interrupt: the work claude
// runs stops too; what it asks the user is withdrawn, and its top-level
// text dropped until a turn starts.
func (s *session) stopLocked(ts []*turn, interrupt bool) {
	var drop []string
	for _, t := range ts {
		switch {
		case !t.active:
			// Its steers wait in claude's queue with it.
			drop = append(drop, slices.Sorted(maps.Keys(t.ids))...)
			s.settleLocked(t, cancelled(t))
		case t.held != nil:
			s.settleLocked(t, cancelled(t)) // its result came: nothing to wait for
		case !t.cancelled:
			t.cancelled = true
			// Steers claude hasn't taken would run after the interrupt, as
			// a turn of their own; dropping one it took does nothing.
			drop = append(drop, slices.Sorted(maps.Keys(t.steers))...)
			s.bg.Go(func() { s.backstop(t) })
		}
	}
	if interrupt {
		s.dropTop = true
		// What it asks the user is moot now; what a background subagent,
		// or one it runs, asks isn't, as the cancel spares it.
		s.withdrawLocked(func(a *ask) bool { return a.agent == "" || !s.kids.inBackground(a.agent) })
	}
	if len(drop) == 0 && !interrupt {
		return
	}
	// The drops, then the interrupt, in their place among the turns'
	// writes: after the user messages of the turns they stop, before any
	// written since. They go one after another with no wait between, as
	// claude-agent-acp writes them (the order a recording of it showed).
	prev, mine := s.nextWriteLocked()
	if !s.bg.Go(func() { s.writeStop(prev, mine, drop, interrupt) }) {
		close(mine) // closed: nothing more is written
	}
}

// writeStop writes claude the drops of the queued user messages drop and,
// if asked, an interrupt, once prev is written; mine is closed once they
// are, and then their answers are waited for.
func (s *session) writeStop(prev <-chan struct{}, mine chan struct{}, drop []string, interrupt bool) {
	<-prev
	var calls []*claudeagent.Call
	for _, id := range drop {
		// Begun, not CancelAsyncMessage: each is written before the
		// next and the interrupt, with no wait for its answer.
		c, err := s.cs.Begin(s.a.ctx, "cancel_async_message", struct {
			MessageUUID string `json:"message_uuid"`
		}{id})
		if err != nil {
			s.log.Info("dropping a queued prompt", "err", err)
			continue
		}
		calls = append(calls, c)
	}
	var stop *claudeagent.Call
	if interrupt {
		var err error
		if stop, err = s.cs.Begin(s.a.ctx, "interrupt", struct{}{}); err != nil {
			s.log.Info("interrupting claude", "err", err)
		}
	}
	close(mine)
	for _, c := range calls {
		if err := c.Wait(nil); err != nil {
			s.log.Info("dropping a queued prompt", "err", err)
		}
	}
	if stop == nil {
		return
	}
	var r claudeagent.InterruptReceipt
	if err := stop.Wait(&r); err != nil {
		s.log.Info("interrupting claude", "err", err)
	} else if len(r.StillQueued) > 0 {
		s.log.Info("claude still has queued messages after the interrupt", "count", len(r.StillQueued))
	}
}

// backstop answers t cancelled if claude says nothing more of it. It
// waits out its time even when t ends first (a select on both could pick
// the timer when both are ready) and then does nothing, so it never ends
// the calls of a turn that started since.
func (s *session) backstop(t *turn) {
	select {
	case <-s.a.after(backstop):
	case <-s.read: // claude is gone: the reader settled t
		return
	}
	s.mu.Lock()
	over := t.settled
	s.mu.Unlock()
	if over {
		return
	}
	s.tools.end(endedCancel)
	s.mu.Lock()
	s.settleLocked(t, cancelled(t))
	s.mu.Unlock()
}

// steer is _session/steering: a message for the running turn.
func (a *agent) steer(ctx context.Context, params json.RawMessage) (*steering.Response, error) {
	var req steering.Request[acp.ContentBlock]
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, invalidParams("steering: %v", err)
	}
	if len(req.Prompt) == 0 {
		return nil, invalidParams("steering: an empty prompt")
	}
	m := readMeta[struct {
		Steering struct {
			IdleBehavior any `json:"idleBehavior"`
		} `json:"steering"`
	}](req.Meta)
	if idle := m.Steering.IdleBehavior; idle != nil && !req.PromptRequired() {
		return nil, invalidParams("steering: no idle behaviour %v", idle)
	}
	s, err := a.session(acp.SessionID(req.SessionID))
	if err != nil {
		return nil, err
	}
	content, err := toClaude(req.Prompt)
	if err != nil {
		return nil, err
	}
	injected, err := s.inject(ctx, content)
	switch {
	case err != nil:
		return nil, err
	case injected:
		return &steering.Response{Outcome: steering.Injected}, nil
	case req.PromptRequired():
		return &steering.Response{Outcome: steering.PromptRequired, Reason: "noRunningTurn"}, nil
	}
	if _, err := s.send(ctx, content); err != nil { // a turn no one waits on
		return nil, err
	}
	return &steering.Response{Outcome: steering.StartedNewTurn}, nil
}

// inject hands content to the turn claude runs, or is about to, as a
// message claude takes now; while a permission or question is open, at
// the next point it may, so the open request isn't cancelled. The turn
// then ends at a result that names the message. false: no turn.
func (s *session) inject(ctx context.Context, content []claudeagent.Block) (bool, error) {
	s.mu.Lock()
	t := s.active
	if t == nil && len(s.turns) > 0 {
		t = s.turns[0]
	}
	// A steer the client sent before a cancel joins no turn: send then
	// answers its turn cancelled.
	if t == nil || t.cancelled || s.closed || s.cancelledBeforeLocked(ctx) {
		s.mu.Unlock()
		return false, nil
	}
	id := s.a.newID()
	t.ids[id], t.steers[id] = true, true
	priority := claudeagent.PriorityNow
	if len(s.open) > 0 {
		priority = claudeagent.PriorityLater
	}
	prev, mine := s.nextWriteLocked()
	s.mu.Unlock()
	<-prev
	_, err := s.cs.Send(ctx, claudeagent.UserInput{Content: content, UUID: id, Priority: priority})
	close(mine)
	if err != nil {
		s.mu.Lock()
		delete(t.steers, id)
		s.mu.Unlock()
		if errors.Is(err, claudeagent.ErrClosed) {
			return false, errEnded
		}
		return false, fmt.Errorf("writing the steer to claude: %w", err)
	}
	return true, nil
}
