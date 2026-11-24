package claudeacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent"
)

// Config is how the agent runs claude.
type Config struct {
	CLIPath string // "" = claude, looked up on PATH
	// ConfigDir is claude's config folder, where its transcripts are; ""
	// = $CLAUDE_CONFIG_DIR, else ~/.claude. When set, each claude gets it
	// as CLAUDE_CONFIG_DIR, so it writes where the agent reads.
	ConfigDir string
	Spawn     claudeagent.Spawner // nil = claudeagent.Exec
	// Log is the agent's log; nil = none. The agent's own lines never hold
	// a prompt or a token, but claude's stderr lines go to it too (at
	// Info, cut at 1 KiB, rate-limited), and those may quote one.
	Log *slog.Logger
	// SharedGroup runs each claude in this process's group
	// (claudeagent.Options.SharedGroup), so a client that stops the agent
	// by killing its group stops every claude with it. Main always sets it.
	SharedGroup bool
}

// readLimit caps a message from the client: a prompt with images can run
// to tens of MiB. A larger one is skipped (a request is answered Invalid
// request) and the connection goes on.
const readLimit = 64 << 20

// agent is the ACP agent: the client's connection and the sessions open
// on it, one claude each.
type agent struct {
	acp.UnimplementedAgent
	client *acp.ClientConn
	ctx    context.Context //nolint:containedctx // ends with the client's connection
	cfg    Config
	log    *slog.Logger
	after  func(time.Duration) <-chan time.Time // the clock; tests give a fake one
	newID  func() string                        // session and message ids; tests give counted ones
	// pause, when a test sets it, runs at points of a prompt's way to
	// claude: "register" as its turn is about to be queued (a prompt slow
	// to set up), "write" as its user message is about to be written (a
	// big one being encoded); "ask" as a permission or question claude
	// asks is about to be put to the user.
	pause func(at string)

	mu        sync.Mutex
	caps      clientCaps
	sessions  map[string]*session
	reopening map[string]*reopener     // by session id: one load or resume of each at a time
	closing   map[string]chan struct{} // by session id: closed once its close is done

	windows windows // the context windows results have said (window.go)
}

// clientCaps is what the client said it handles, of what changes how a
// session runs.
type clientCaps struct {
	forms        bool // form elicitation, which AskUserQuestion needs
	subagents    bool // subagents as sessions of their own
	subagentText bool // subagents' text and thinking (_meta "subagent-transcript")
	notices      bool // ACP's notices, for what claude says of its own
	urls         bool // URL elicitation, which an MCP server's sign-in needs
	compaction   bool // ACP's compaction updates, for a compaction
	booleans     bool // boolean config options
}

// Serve runs the agent on r and w, the client's streams, until the client
// goes or ctx ends; then it closes every session, and each claude exits.
// It returns nil once the client has gone (r ended), ctx's error when ctx
// ended first, and otherwise what broke the connection.
func Serve(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	return newAgent(cfg).serve(ctx, r, w)
}

func newAgent(cfg Config) *agent {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &agent{cfg: cfg, log: log, after: time.After, newID: newUUID, sessions: map[string]*session{}}
}

// serve runs the agent as Serve says. The agent's own context outlives
// ctx until the sessions are closed, so closing them still interrupts
// claude.
func (a *agent) serve(ctx context.Context, r io.Reader, w io.Writer) error {
	actx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	a.ctx = actx
	// Not w itself: the connection would close it as it ends, and a
	// client that reads it until the agent exits would stop waiting while
	// the sessions still close.
	conn := acp.NewClientConn(r, struct{ io.Writer }{w}, func(c *acp.ClientConn) acp.Agent {
		a.client = c
		return a
	}, acp.WithReadLimit(readLimit), acp.WithLogger(a.log))
	select {
	case <-conn.Done():
	case <-ctx.Done():
		_ = conn.Close()
		<-conn.Done()
	}
	a.mu.Lock()
	open := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		open = append(open, s)
	}
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range open {
		wg.Go(s.close)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := errors.Unwrap(conn.Err()); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("claudeacp: the client's connection: %w", err)
	}
	return nil
}

func (a *agent) Initialize(_ context.Context, req *acp.InitializeRequest) (*acp.InitializeResponse, error) {
	var caps clientCaps
	if c := req.ClientCapabilities; c != nil {
		caps.forms = c.Elicitation != nil && c.Elicitation.Form != nil
		caps.urls = c.Elicitation != nil && c.Elicitation.URL != nil
		caps.notices = c.Session != nil && c.Session.Notices != nil
		caps.compaction = c.Session != nil && c.Session.Compaction != nil
		caps.booleans = c.Session != nil && c.Session.ConfigOptions != nil && c.Session.ConfigOptions.Boolean != nil
		caps.subagents = c.Subagents != nil
		caps.subagentText = readMeta[struct {
			SubagentTranscript bool `json:"subagent-transcript"`
		}](c.Meta).SubagentTranscript
	}
	a.mu.Lock()
	a.caps = caps
	a.mu.Unlock()
	return &acp.InitializeResponse{
		ProtocolVersion: acp.LatestProtocolVersion,
		AgentInfo:       &acp.Implementation{Name: Name, Title: "Claude (Go)", Version: Version},
		AgentCapabilities: &acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: &acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			MCPCapabilities:    &acp.MCPCapabilities{HTTP: true, SSE: true},
			SessionCapabilities: &acp.SessionCapabilities{
				Close:                 &acp.SessionCloseCapabilities{},
				Resume:                &acp.SessionResumeCapabilities{},
				Fork:                  &acp.SessionForkCapabilities{},
				List:                  &acp.SessionListCapabilities{},
				Delete:                &acp.SessionDeleteCapabilities{},
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
				// The capability itself says append (claude's
				// appendSystemPrompt); its mode adds override.
				SystemPrompt: &acp.SessionSystemPromptCapabilities{
					Mode: &acp.SessionSystemPromptModeCapabilities{Override: &acp.SessionSystemPromptOverrideCapabilities{}},
				},
			},
			// Prompts sent during a turn are queued, as claude queues them.
			Meta: claudeCodeMeta{PromptQueueing: true}.meta(),
		},
		Meta: steering.Advertise(nil),
	}, nil
}

func (a *agent) clientCaps() clientCaps {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.caps
}

// session is the open session id, or an invalidParams error.
func (a *agent) session(id acp.SessionID) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[string(id)]; s != nil {
		return s, nil
	}
	return nil, &acp.Error{Code: acp.ErrorCodeInvalidParams, Message: fmt.Sprintf("no open session %q", id)}
}

// forget removes s from the open sessions.
func (a *agent) forget(s *session) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
}

// closingStart removes s from the open sessions as its close starts: its
// id is closing, and waitClosed waits until done says the close is done
// (claude has exited).
func (a *agent) closingStart(s *session) (done func()) {
	ch := make(chan struct{})
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
	if a.closing == nil {
		a.closing = map[string]chan struct{}{}
	}
	if a.closing[s.id] != nil {
		return func() {} // a close of it is under way: it says when done
	}
	a.closing[s.id] = ch
	return func() {
		a.mu.Lock()
		delete(a.closing, s.id)
		a.mu.Unlock()
		close(ch)
	}
}

// waitClosed waits until no close of session id is under way, so what
// comes next (a delete of its files, a claude resuming it) doesn't meet
// a claude still writing its transcript as it ends.
func (a *agent) waitClosed(id string) {
	a.mu.Lock()
	ch := a.closing[id]
	a.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

// update sends the client one of a session's updates. It fails only when
// the client is gone, and then nothing more can be sent.
func (a *agent) update(sid string, u acp.SessionUpdate) {
	_ = a.client.SessionUpdate(a.ctx, &acp.SessionNotification{SessionID: acp.SessionID(sid), Update: u})
}

func (a *agent) Prompt(ctx context.Context, req *acp.PromptRequest) (*acp.PromptResponse, error) {
	s, err := a.session(req.SessionID)
	if err != nil {
		return nil, err
	}
	content, err := toClaude(req.Prompt)
	if err != nil {
		return nil, err
	}
	s.plan.again()
	t, err := s.send(ctx, content)
	if err != nil {
		return nil, err
	}
	for _, b := range req.Prompt {
		if b.Text != nil {
			s.title.heard("", b.Text.Text)
		}
	}
	select {
	case o := <-t.done:
		return o.resp, o.err
	case <-ctx.Done():
		// The client withdrew the prompt ($/cancel_request): it alone
		// stops, not the prompts queued after it. When the client went
		// instead, closing the session interrupts the turn, once (serve).
		if a.client.Err() == nil {
			s.withdrawTurn(t)
		}
		return nil, ctx.Err()
	}
}

func (a *agent) Cancel(ctx context.Context, n *acp.CancelNotification) error {
	if s, err := a.session(n.SessionID); err == nil {
		seq, _ := acp.SequenceFromContext(ctx)
		s.cancel(seq)
	} else if s := a.child(string(n.SessionID)); s != nil {
		s.stopSubagent(string(n.SessionID)) // a subagent's session: that subagent alone
	}
	return nil
}

// CloseSession closes a session; one that isn't open is closed already.
//
//nolint:contextcheck // closing runs to its end: the request's ctx doesn't bound it
func (a *agent) CloseSession(_ context.Context, req *acp.CloseSessionRequest) (*acp.CloseSessionResponse, error) {
	if s, err := a.session(req.SessionID); err == nil {
		s.close()
	}
	return &acp.CloseSessionResponse{}, nil
}

func (a *agent) ExtMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == steering.Method {
		return a.steer(ctx, params)
	}
	switch method {
	case GenerateTitleMethod:
		return a.generateTitle(ctx, params)
	case ContextUsageMethod:
		return a.contextUsage(ctx, params)
	case StopTaskMethod:
		return a.stopTask(ctx, params)
	}
	return a.UnimplementedAgent.ExtMethod(ctx, method, params)
}
