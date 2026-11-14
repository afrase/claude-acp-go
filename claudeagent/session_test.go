package claudeagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/afrase/claude-acp-go/claudeagent"
	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

const initRequest = `{"type":"control_request","request":{"subtype":"initialize"}}`

// answer is claude's success answer to the request Expect saw last.
func answer(payload string) claudetest.Step {
	return claudetest.Send(`{"type":"control_response","response":{"subtype":"success","request_id":"$req","response":` + payload + `}}`)
}

// start starts a session on a fake claude that answers initialize with
// {} and then plays steps.
func start(t *testing.T, o claudeagent.Options, steps ...claudetest.Step) (*claudeagent.Session, *claudetest.Fake) {
	t.Helper()
	return startWith(t, o, `{}`, steps...)
}

func startWith(t *testing.T, o claudeagent.Options, initAnswer string, steps ...claudetest.Step) (*claudeagent.Session, *claudetest.Fake) {
	t.Helper()
	f := claudetest.New(append([]claudetest.Step{claudetest.Expect(initRequest), answer(initAnswer)}, steps...)...)
	o.CLIPath, o.Env, o.Spawner = "/claude", []string{}, f.Spawn
	s, err := claudeagent.Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, f
}

// finish waits for the fake to play its script and for claude's requests
// to be answered, closes the session and checks that nothing more was
// written to the fake, which exited with 0.
func finish(t *testing.T, s *claudeagent.Session, f *claudetest.Fake) {
	t.Helper()
	select {
	case <-f.Played():
	case <-f.Exited():
	case <-time.After(5 * time.Second): // a guard, not an order
		t.Error("the script didn't play through")
	}
	claudeagent.WaitHandlers(s)
	_ = s.Close()
	if err := s.Wait(); err != nil {
		t.Error(err)
	}
	if err := f.Err(); err != nil {
		t.Error(err)
	}
}

// next is the session's next message.
func next(t *testing.T, s *claudeagent.Session) claudeagent.Message {
	t.Helper()
	select {
	case m, ok := <-s.Messages():
		if !ok {
			t.Fatal("the messages ended")
		}
		return m
	case <-time.After(5 * time.Second): // a guard, not an order
		t.Fatal("no message")
	}
	return nil
}

// Initialize goes first and carries Init; claude's answer is kept, and
// one with no payload is an empty answer.
func TestStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		init    claudeagent.Init
		request string
		answer  string
		model   string
	}{
		{"init", claudeagent.Init{Title: "T", PerTaskStopAffordance: true, Extra: map[string]any{"workspaceTrust": true}},
			`{"subtype":"initialize","title":"T","perTaskStopAffordance":true,"workspaceTrust":true}`, `{"current_model":"opus"}`, "opus"},
		{"null", claudeagent.Init{}, `{"subtype":"initialize"}`, `null`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := claudetest.New(claudetest.Expect(`{"type":"control_request","request":`+tc.request+`}`), answer(tc.answer))
			s, err := claudeagent.Start(context.Background(), claudeagent.Options{CLIPath: "/claude", Env: []string{}, Spawner: f.Spawn, Init: tc.init})
			if err != nil {
				t.Fatal(err)
			}
			if s.Init() == nil || s.Init().CurrentModel != tc.model {
				t.Errorf("%+v", s.Init())
			}
			finish(t, s, f)
		})
	}
	if _, err := claudeagent.Start(context.Background(), claudeagent.Options{Init: claudeagent.Init{Extra: map[string]any{"title": 1}}}); err == nil {
		t.Error("started with Extra setting the title")
	}
}

// An answer to initialize that names no account is a warning in the log:
// claude may have started without the user's login.
func TestStartWithoutAccount(t *testing.T) {
	for _, tc := range []struct {
		answer string
		warn   bool
	}{
		{`{"account":{"subscriptionType":"max"}}`, false},
		{`{"account":{"apiKeySource":"ANTHROPIC_API_KEY"}}`, false},
		{`{"account":{}}`, true},
		{`{"claude_code_version":"9.9.9"}`, true},
		{`null`, true},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			var log syncBuffer
			s, f := startWith(t, claudeagent.Options{Log: slog.New(slog.NewTextHandler(&log, nil))}, tc.answer)
			got := log.String() // Start logs before it returns
			finish(t, s, f)
			if warned := strings.Contains(got, `level=WARN msg="claude's answer to initialize names no account`); warned != tc.warn {
				t.Errorf("warned %v:\n%s", warned, got)
			}
		})
	}
}

// claude's version is logged from its answer to initialize: no second
// claude runs to ask it.
func TestStartLogsVersion(t *testing.T) {
	var log syncBuffer
	s, f := startWith(t, claudeagent.Options{Log: slog.New(slog.NewTextHandler(&log, nil))},
		`{"claude_code_version":"2.1.400","account":{"subscriptionType":"max"}}`)
	got := log.String()
	finish(t, s, f)
	if !strings.Contains(got, `level=INFO msg=claude version=2.1.400`) {
		t.Errorf("no version logged:\n%s", got)
	}
}

// syncBuffer is a log the session's goroutines write and a test reads.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A request claude sends before initialize is written is answered only
// after it: claude takes its first stdin line for the session's start.
func TestInitializeFirst(t *testing.T) {
	f := claudetest.New(
		claudetest.Send(`{"type":"control_request","request_id":"early","request":{"subtype":"ping"}}`),
		claudetest.Expect(initRequest),
		answer(`{}`),
		claudetest.Expect(`{"type":"control_response","response":{"subtype":"error","request_id":"early"}}`),
	)
	s, err := claudeagent.Start(context.Background(), claudeagent.Options{CLIPath: "/claude", Env: []string{}, Spawner: f.Spawn})
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, f)
}

// Close ends callbacks' contexts and fails requests waiting for an answer
// at once, not when claude exits: after its stdin closes claude may take
// seconds more to finish its turn.
func TestCloseBeforeExit(t *testing.T) {
	asked, returned := make(chan struct{}), make(chan struct{})
	wait := func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(asked)
		<-ctx.Done()
		close(returned)
		return nil, ctx.Err()
	}
	release := make(chan struct{})
	s, f := start(t, claudeagent.Options{OnControl: wait},
		sendRequest(`{"subtype":"x"}`),
		claudetest.Expect(`{"type":"control_request","request":{"subtype":"get_settings"}}`),
		claudetest.Send(`{"type":"system","subtype":"status","status":"requesting"}`),
		claudetest.ExpectEOF(),
		claudetest.Until(release),
	)
	<-asked
	answer := make(chan error, 1)
	go func() { answer <- s.Request(context.Background(), "get_settings", nil, nil) }()
	next(t, s) // the request is written, and waits
	closed := make(chan struct{})
	go func() {
		_ = s.Close()
		close(closed)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second): // a guard, not an order
		t.Fatal("the callback's context didn't end")
	}
	if err := <-answer; !errors.Is(err, claudeagent.ErrClosed) {
		t.Errorf("the request: %v", err)
	}
	select {
	case <-f.Exited():
		t.Error("ended only once claude exited")
	default:
	}
	close(release)
	<-closed
	if err := s.Wait(); err != nil || f.Err() != nil {
		t.Errorf("%v %v", err, f.Err())
	}
}

// A claude that ends before it answers initialize fails the start with
// how it ended; an error answer fails it with claude's reason.
func TestStartFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		steps  []claudetest.Step
		want   string
		exited bool // ErrExitedBeforeInit
	}{
		{"exits", []claudetest.Step{claudetest.Expect(initRequest), claudetest.Exit(1, "No conversation found with session ID: s1\n")},
			"claude ended before answering initialize: claude exited with code 1: No conversation found with session ID: s1", true},
		{"exits with 0", []claudetest.Step{claudetest.Expect(initRequest), claudetest.Exit(0, "")},
			"claude ended before answering initialize", true},
		{"refuses", []claudetest.Step{claudetest.Expect(initRequest), claudetest.Send(`{"type":"control_response","response":{"subtype":"error","request_id":"$req","error":"bad hooks"}}`)},
			"claude refused initialize: bad hooks", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := claudetest.New(tc.steps...)
			_, err := claudeagent.Start(context.Background(), claudeagent.Options{CLIPath: "/claude", Env: []string{}, Spawner: f.Spawn})
			if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
				t.Fatalf("%v", err)
			}
			if errors.Is(err, claudeagent.ErrExitedBeforeInit) != tc.exited {
				t.Errorf("errors.Is(%v, ErrExitedBeforeInit) = %v", err, !tc.exited)
			}
			<-f.Exited()
		})
	}
}

// Writing to a claude that has exited, before anyone closed the session,
// is ErrClosed, not the pipe's own error.
func TestWriteAfterClaudeExited(t *testing.T) {
	s, f := start(t, claudeagent.Options{}, claudetest.Exit(1, "out of memory\n"))
	<-f.Exited()
	if _, err := s.Send(context.Background(), claudeagent.UserInput{Text: "hi"}); !errors.Is(err, claudeagent.ErrClosed) {
		t.Errorf("Send: %v", err)
	}
	var ee *claudeagent.ExitError
	if err := s.Wait(); !errors.As(err, &ee) || ee.Code != 1 {
		t.Errorf("Wait: %v", err)
	}
}

// A claude that exits before initialize can even be written still fails
// the start with how it ended.
func TestStartClaudeGoneBeforeInitialize(t *testing.T) {
	spawn := func(context.Context, claudeagent.Spec) (*claudeagent.Proc, error) {
		return &claudeagent.Proc{
			Stdin: brokenPipe{}, Stdout: io.NopCloser(strings.NewReader("")), Stderr: io.NopCloser(strings.NewReader("fatal: bad flag\n")),
			Process: exited{&claudeagent.ExitError{Code: 2}},
		}, nil
	}
	_, err := claudeagent.Start(context.Background(), claudeagent.Options{Env: []string{}, Spawner: spawn})
	var ee *claudeagent.ExitError
	if !errors.Is(err, claudeagent.ErrExitedBeforeInit) || !errors.As(err, &ee) || ee.Code != 2 || ee.Stderr != "fatal: bad flag" {
		t.Errorf("%v", err)
	}
}

// brokenPipe is the stdin of a claude that has gone.
type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, syscall.EPIPE }
func (brokenPipe) Close() error              { return nil }

// exited is a claude that has exited with err.
type exited struct{ err error }

func (e exited) Wait() error          { return e.err }
func (exited) Signal(os.Signal) error { return os.ErrProcessDone }

// Has is the union of initialize's capabilities and every system/init's,
// and agrees with a system/init by the time it is read.
func TestCapabilities(t *testing.T) {
	s, f := startWith(t, claudeagent.Options{}, `{"capabilities":["a"]}`,
		claudetest.Send(`{"type":"system","subtype":"init","capabilities":["b"]}`),
		claudetest.Send(`{"type":"system","subtype":"init","capabilities":["c"]}`),
	)
	if _, ok := next(t, s).(*claudeagent.SystemInit); !ok || !s.Has("a") || !s.Has("b") {
		t.Error("after the first init")
	}
	if _, ok := next(t, s).(*claudeagent.SystemInit); !ok || !s.Has("a") || !s.Has("b") || !s.Has("c") || s.Has("d") {
		t.Error("after the second")
	}
	finish(t, s, f)
}
