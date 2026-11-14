package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// stdin is claude's stdin as a test sees it: the lines written to it,
// and closed once the session closes it.
type stdin struct {
	mu     sync.Mutex
	lines  []string
	closed chan struct{}
	once   sync.Once
}

func (w *stdin) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, string(p))
	return len(p), nil
}

func (w *stdin) Close() error {
	w.once.Do(func() { close(w.closed) })
	return nil
}

func (w *stdin) written() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.lines...)
}

// untilStdinCloses is a claude that exits once its stdin closes.
type untilStdinCloses struct {
	in          *stdin
	out, errOut io.Closer
}

func (p *untilStdinCloses) Wait() error {
	<-p.in.closed
	_ = p.out.Close()
	_ = p.errOut.Close()
	return nil
}

func (p *untilStdinCloses) Signal(os.Signal) error { return nil }

// A request claude sends before initialize is answered only once
// initialize is written: claude takes its first stdin line for the
// session's start. Start writes initialize before the request is read,
// so the session is made here without it.
func TestWritesWaitForInitialize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		in := &stdin{closed: make(chan struct{})}
		outR, outW := io.Pipe()
		errR, errW := io.Pipe()
		p := &Proc{Stdin: in, Stdout: outR, Stderr: errR, Process: &untilStdinCloses{in, outW, errW}}
		s := newSession(t.Context(), newTransport(p, Options{}), Options{}, nil)
		t.Cleanup(func() { _ = s.Close() }) // in the bubble, which ends with no goroutine left
		if _, err := io.WriteString(outW, `{"type":"control_request","request_id":"early","request":{"subtype":"ping"}}`+"\n"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // the request is handled, its answer held
		if got := in.written(); len(got) != 0 {
			t.Fatalf("written before initialize: %q", got)
		}

		initErr := make(chan error, 1)
		go func() {
			_, err := s.request(t.Context(), ask{"initialize"}, false)
			initErr <- err
		}()
		synctest.Wait()
		got := in.written()
		if len(got) != 2 {
			t.Fatalf("%q", got)
		}
		var first struct {
			Request struct{ Subtype string } `json:"request"`
		}
		var second struct {
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(got[0]), &first) != nil || first.Request.Subtype != "initialize" {
			t.Errorf("first %s", got[0])
		}
		if json.Unmarshal([]byte(got[1]), &second) != nil || second.Response.RequestID != "early" || second.Response.Subtype != "error" {
			t.Errorf("second %s", got[1])
		}

		_ = s.Close()
		if err := <-initErr; !errors.Is(err, ErrClosed) {
			t.Errorf("initialize: %v", err)
		}
	})
}

// writing returns a session whose initialize is written, and its stdin.
func writing(t *testing.T) (*Session, *stdin) {
	t.Helper()
	in := &stdin{closed: make(chan struct{})}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &Proc{Stdin: in, Stdout: outR, Stderr: errR, Process: &untilStdinCloses{in, outW, errW}}
	s := newSession(t.Context(), newTransport(p, Options{}), Options{}, nil)
	s.initDone()
	t.Cleanup(func() { _ = s.Close() })
	return s, in
}

// A write with an ended ctx is never written, though initialize is: not
// half the time, as a select among ready cases would have it.
func TestWriteWithEndedContext(t *testing.T) {
	s, in := writing(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		if err := s.write(ctx, map[string]string{"type": "user"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("write: %v", err)
		}
	}
	if got := in.written(); len(got) != 0 {
		t.Errorf("written: %q", got)
	}
}

// An answer that has come is the answer, though ctx has ended too; the
// request isn't withdrawn then.
func TestAnswerBeatsEndedContext(t *testing.T) {
	s, in := writing(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 100 {
		c := &Call{s: s, ctx: ctx, id: "r1", subtype: "ping", ch: make(chan reply, 1)}
		c.ch <- reply{payload: json.RawMessage(`{"ok":true}`)}
		if payload, err := c.answer(); err != nil || string(payload) != `{"ok":true}` {
			t.Fatalf("answer: %s %v", payload, err)
		}
	}
	if got := in.written(); len(got) != 0 {
		t.Errorf("withdrawn: %q", got)
	}
}

// stuckStdin is the stdin of a claude that has stopped reading it: a
// write to it waits until it is closed.
type stuckStdin struct {
	*io.PipeWriter
	closed chan struct{}
	once   sync.Once
}

func (w *stuckStdin) Close() error {
	w.once.Do(func() { close(w.closed) })
	return w.PipeWriter.Close()
}

// exitsOnClose is a claude that exits once its stdin closes.
type exitsOnClose struct {
	in          *stuckStdin
	out, errOut io.Closer
}

func (p *exitsOnClose) Wait() error {
	<-p.in.closed
	_ = p.out.Close()
	_ = p.errOut.Close()
	return nil
}

func (p *exitsOnClose) Signal(os.Signal) error { return nil }

// With a write stuck on claude's full stdin, a request still gives up as
// its ctx says, rather than waiting behind the write for good; Close then
// ends the stuck write.
func TestRequestNotHeldByStuckWrite(t *testing.T) {
	_, inW := io.Pipe() // no one reads it
	in := &stuckStdin{PipeWriter: inW, closed: make(chan struct{})}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &Proc{Stdin: in, Stdout: outR, Stderr: errR, Process: &exitsOnClose{in, outW, errW}}
	s := newSession(t.Context(), newTransport(p, Options{}), Options{}, nil)
	s.initDone()
	t.Cleanup(func() { _ = s.Close() })

	sent := make(chan error, 1)
	go func() {
		_, err := s.Send(context.Background(), UserInput{Text: strings.Repeat("x", 1<<20)})
		sent <- err
	}()
	for len(s.t.wslot) == 0 { // the Send holds the slot
		runtime.Gosched()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	interrupted := make(chan error, 1)
	go func() {
		_, err := s.Interrupt(ctx)
		interrupted <- err
	}()
	select {
	case err := <-interrupted:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Interrupt: %v", err)
		}
	case <-time.After(3 * time.Second): // a guard, not an order
		t.Fatal("Interrupt with a 200 ms ctx still waits behind the stuck write")
	}
	_ = s.Close()
	if err := <-sent; !errors.Is(err, ErrClosed) {
		t.Errorf("the stuck Send: %v", err)
	}
}
