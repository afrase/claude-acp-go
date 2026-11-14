package claudeacp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"
	"uuid"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// session is an ACP session: one claude, its turns, and what its stream
// has shown so far. The turns are its own, under mu; each part below
// them keeps its state under a lock of its own and owns what changes it.
//
// The order its locks nest in: a lock may be taken while one to its left
// on a line is held, never the other way round.
//
//	mu → kids.mu
//	tools.mu → kids.mu
//	cfg.changing → cfg.mu
//	cfg.changing → win.mu → agent.windows.mu
//
// The rest (plan.mu, cmds.mu, title.mu, hmu, bg.mu, stderr's) are
// leaves, as are the locks at the lines' ends: one may be taken with
// any lock above held (cancel starts its goroutines under mu), and none
// is held while another is taken but for the client connection's.
// tools.mu, kids.mu, plan.mu, cmds.mu and win.mu send the client their
// updates while held, to keep the updates' order, so the connection's
// own lock is always taken last.
type session struct {
	a      *agent
	id     string // claude's session id too
	cs     *claudeagent.Session
	log    *slog.Logger
	stderr *stderrLog // claude's

	mu       sync.Mutex
	turns    []*turn           // unsettled prompts, in the order written
	active   *turn             // the one claude runs, from its echo; nil between turns
	dropTop  bool              // after a cancel, until a turn starts: the stopped work's top-level text goes
	owedIdle int               // idles still to come after results that ended a turn
	open     map[*ask]struct{} // the permission and question requests open
	closed   bool              // closed or gone: no more prompts
	// written orders what the turns write to claude (user messages, a
	// cancel's drops and interrupt) as the turns changed under mu: each
	// writer takes its place under mu (nextWriteLocked), waits for the
	// one before it to be written, then writes. So claude's queue is in
	// the turns' order, and a cancel's interrupt never overtakes the
	// prompt it stops.
	written chan struct{}
	// cancelSeq is where the latest session/cancel came among the
	// client's messages (acp.SequenceFromContext): a prompt or steer from
	// before it that registers after it is cancelled already.
	cancelSeq uint64

	tools tools     // the tool calls shown
	kids  subagents // its subagents, through which every update of a stream goes

	cwd   string
	cfg   config // its modes and options
	cmds  commands
	title titles
	plan  plan

	win  window        // the context window's size
	st   stream        // the reader's own
	read chan struct{} // closed once the reader is done
	bg   background    // its goroutines besides the reader's

	hmu     sync.Mutex
	handled uint64        // claude's messages the reader has handled
	tick    chan struct{} // closed, and made anew, at each
}

func (a *agent) NewSession(ctx context.Context, req *acp.NewSessionRequest) (*acp.NewSessionResponse, error) {
	o, err := a.options(setup{cwd: req.Cwd, dirs: req.AdditionalDirectories, mcpServers: req.MCPServers, systemPrompt: req.SystemPrompt, meta: req.Meta})
	if err != nil {
		return nil, err
	}
	o.SessionID = a.newID() // so the ACP session's id is claude's
	s, err := a.start(ctx, o.SessionID, o)
	if err != nil {
		return nil, err
	}
	a.out.after(s.id, s.cmds.sendAll)
	modes, opts := s.cfg.state()
	return &acp.NewSessionResponse{SessionID: acp.SessionID(s.id), Modes: modes, ConfigOptions: opts}, nil
}

// start runs claude for session id and opens the session once claude has
// answered initialize.
func (a *agent) start(ctx context.Context, id string, o claudeagent.Options) (*session, error) {
	s := &session{a: a, id: id, cwd: o.Cwd, log: a.log.With("session", id), read: make(chan struct{}), tick: make(chan struct{}), open: map[*ask]struct{}{}}
	s.written = make(chan struct{})
	close(s.written) // nothing to wait for
	s.st.init()
	s.kids.init(id, a.clientCaps().subagents, a.update)
	s.tools.init(o.Cwd, &s.kids, a.newID)
	s.tools.compactUpdates = a.clientCaps().compaction
	s.plan.init(s.update)
	s.cmds.send = s.update
	o.Hooks = map[claudeagent.HookEvent][]claudeagent.HookMatcher{
		claudeagent.HookPostToolUse:     {{Fn: s.postToolUse}},
		claudeagent.HookPostModelSwitch: {{Fn: s.postModelSwitch}},
	}
	if s.tools.compactUpdates { // only ACP's compaction updates carry the summary
		o.Hooks[claudeagent.HookPostCompact] = []claudeagent.HookMatcher{{Fn: s.postCompact}}
	}
	o.CanUseTool, o.OnElicitation = s.canUseTool, s.elicit
	s.stderr = newStderrLog(s.log, time.Now)
	o.Stderr = s.stderr.line
	cs, err := claudeagent.Start(ctx, o)
	if err != nil {
		return nil, a.startError(o.Resume, o.Cwd, err)
	}
	s.cs = cs
	s.cfg.send, s.cfg.log, s.cfg.claude = s.update, s.log, cs
	s.cfg.init(o, cs.Init(), a.clientCaps().booleans)
	if ir := cs.Init(); ir != nil {
		s.cmds.list = ir.Commands
	}
	s.win.send, s.win.provider = s.update, providerKey(o.Env)
	s.seedWindow(ctx, 0)
	a.mu.Lock()
	a.sessions[id] = s
	a.mu.Unlock()
	go s.reader()
	return s, nil
}

// reader maps claude's messages to the client's updates and turns' ends
// until claude's output ends.
func (s *session) reader() {
	defer close(s.read)
	for m := range s.cs.Messages() {
		s.handle(m)
		s.hmu.Lock()
		s.handled++
		close(s.tick)
		s.tick = make(chan struct{})
		s.hmu.Unlock()
	}
	err := s.cs.Wait() // and its stderr read through
	s.stderr.flush()
	_ = s.cs.Close()
	s.tools.end(endedExit)
	s.kids.gone()
	s.mu.Lock()
	gone := !s.closed
	s.closed = true // no more turns join
	s.mu.Unlock()
	if gone {
		// claude died under the session: the session goes, then its
		// turns fail, so a prompt sent once they have is told there is
		// no session rather than, now and then, that it has ended.
		s.log.Warn("claude exited", "err", err)
		s.a.forget(s)
		s.mu.Lock()
		for _, t := range slices.Clone(s.turns) {
			s.settleLocked(t, outcome{err: died})
		}
		s.mu.Unlock()
	}
}

// caughtUp waits until the reader has handled the messages claude wrote
// before the request ctx is for, as claude handles requests beside its
// messages: so the request finds what they said, such as the start of
// the subagent it comes from. It waits no more once claude is gone or
// ctx ends.
func (s *session) caughtUp(ctx context.Context) {
	n, ok := claudeagent.MessagesBefore(ctx)
	for ok {
		s.hmu.Lock()
		done, tick := s.handled >= n, s.tick
		s.hmu.Unlock()
		if done {
			return
		}
		select {
		case <-tick:
		case <-ctx.Done():
			return
		case <-s.read:
			return
		}
	}
}

// died is what a turn gets when claude exits under it.
var died = &acp.Error{Code: acp.ErrorCodeInternalError, Message: "The Claude process exited unexpectedly. Please start a new session.",
	Data: []byte(`{"errorKind":"transport_lost"}`)}

// errEnded is what a prompt for a session closing gets.
var errEnded = &acp.Error{Code: acp.ErrorCodeInternalError, Message: "The Claude session has ended. Please start a new session."}

// send writes a prompt's user message and queues its turn. ctx is the
// prompt's request's: a prompt the client cancelled before its turn got
// here is answered cancelled, and claude never gets it.
func (s *session) send(ctx context.Context, content []claudeagent.Block) (*turn, error) {
	s.a.paused("register")
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errEnded
	}
	t := newTurn(s.a.newID())
	if s.cancelledBeforeLocked(ctx) {
		s.settleLocked(t, cancelled(t))
		s.mu.Unlock()
		return t, nil
	}
	s.turns = append(s.turns, t)
	prev, mine := s.nextWriteLocked()
	s.mu.Unlock()
	<-prev
	s.a.paused("write")
	_, err := s.cs.Send(ctx, claudeagent.UserInput{Content: content, UUID: t.id})
	close(mine)
	if err != nil {
		s.mu.Lock()
		s.settleLocked(t, outcome{err: err})
		s.mu.Unlock()
		if errors.Is(err, claudeagent.ErrClosed) {
			return nil, errEnded
		}
		return nil, fmt.Errorf("writing the prompt to claude: %w", err)
	}
	return t, nil
}

func (a *agent) paused(at string) {
	if a.pause != nil {
		a.pause(at)
	}
}

// cancelledBeforeLocked says a session/cancel came after the request
// ctx is for, which is still setting up: acp-go starts a request's
// handler before it reads on, but the cancel's runs beside it.
func (s *session) cancelledBeforeLocked(ctx context.Context) bool {
	seq, ok := acp.SequenceFromContext(ctx)
	return ok && seq < s.cancelSeq
}

// nextWriteLocked takes the next place among the turns' writes to claude:
// the writer waits for prev, writes, and closes mine, whether or not
// its write went.
func (s *session) nextWriteLocked() (prev <-chan struct{}, mine chan struct{}) {
	prev, mine = s.written, make(chan struct{})
	s.written = mine
	return prev, mine
}

// closeWait is how long close waits for claude to take an interrupt. With
// claudeagent's 2 s for claude to exit once its stdin ends, a close takes
// under the 5 s a client may give an agent before it kills its group, and
// claude with it (Main).
const closeWait = 2 * time.Second

// close ends the session: a running turn is interrupted, every turn
// answered cancelled, and claude closed. It returns once claude has
// exited and the reader is done, and does nothing the second time.
func (s *session) close() {
	s.a.forget(s)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.read
		s.bg.wait()
		return
	}
	s.closed = true
	running := len(s.turns) > 0
	if s.active != nil {
		s.active.cancelled = true // its result, if the interrupt brings one, ends it cancelled
	}
	s.mu.Unlock()
	if running {
		// claude would finish the turn before it took stdin's end.
		ctx, cancel := context.WithCancel(s.a.ctx)
		go func() {
			select {
			case <-s.a.after(closeWait):
				cancel()
			case <-ctx.Done():
			}
		}()
		if _, err := s.cs.Interrupt(ctx); err != nil {
			s.log.Info("interrupting claude to close", "err", err)
		}
		cancel()
	}
	s.mu.Lock()
	for _, t := range slices.Clone(s.turns) {
		s.settleLocked(t, cancelled(t))
	}
	s.mu.Unlock()
	_ = s.cs.Close()
	<-s.read
	s.bg.wait()
}

// background is a session's goroutines besides its reader: a cancel's
// requests and backstop, a subagent's stop, the context window's answer,
// a look for its title. close waits for them; once it has, no more
// start, as claude is gone and each would only fail.
type background struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
}

// Go runs fn on a goroutine of the session's, unless it has closed;
// false: it hasn't run fn.
func (b *background) Go(fn func()) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.wg.Go(fn)
	}
	return !b.closed
}

// wait waits for the goroutines running, and starts no more.
func (b *background) wait() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.wg.Wait()
}

// update sends the client an update of the session's own.
func (s *session) update(u acp.SessionUpdate) { s.a.update(s.id, u) }

// newUUID is a random (version 4) UUID: a new message's id.
func newUUID() string { return uuid.NewV4().String() }
