package claudeacp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// taking is a claude that takes every user message and does nothing with
// it: a line kept among the requests it answered (Responded), in order.
func taking(steps ...claudetest.Step) *claudetest.Fake {
	f := fake(steps...)
	f.Respond(`{"type":"user"}`, `{"type":"keep_alive"}`)
	return f
}

// A session/cancel sent right after a session/prompt cancels it, however
// little after: the prompt is answered cancelled. One cancelled before its
// turn was queued never reaches claude.
func TestCancelRightAfterPrompt(t *testing.T) {
	for _, slow := range []bool{false, true} {
		name := "at once"
		if slow {
			name = "a prompt slow to set up"
		}
		t.Run(name, func(t *testing.T) {
			f := taking()
			p := start(t, nil, f)
			sid := p.open(t)
			if slow {
				p.agent.pause = func(at string) {
					if at == "register" {
						time.Sleep(5 * time.Millisecond)
					}
				}
			}
			for i := range 50 {
				w := p.prompt(t, sid, "go")
				if err := p.Cancel(t.Context(), &acp.CancelNotification{SessionID: sid}); err != nil {
					t.Fatal(err)
				}
				if got := stopReason(w); got != "cancelled" {
					t.Fatalf("prompt %d: %s", i, got)
				}
			}
			if !slow {
				return
			}
			for _, l := range responded(f) {
				if strings.Contains(l, `"type":"user"`) {
					t.Errorf("claude got a cancelled prompt: %s", l)
				}
			}
		})
	}
}

// A cancel's drop and interrupt reach claude after the user message of
// the prompt they stop, though the cancel came while that message was
// still being written.
func TestCancelDuringWrite(t *testing.T) {
	f := taking()
	p := start(t, nil, f)
	sid := p.open(t)
	writing, release := make(chan struct{}), make(chan struct{})
	p.agent.pause = func(at string) {
		if at == "write" {
			close(writing)
			<-release
		}
	}
	w := p.prompt(t, sid, "go")
	wait(t, writing, "the prompt being written")
	if err := p.Cancel(t.Context(), &acp.CancelNotification{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the prompt cancelled", func() bool { ids, _ := p.queue(t, sid); return len(ids) == 0 })
	time.Sleep(20 * time.Millisecond) // a drop that doesn't wait would have gone by now
	close(release)
	stopsWith(t, w, "cancelled")
	eventually(t, "the prompt, its drop and the interrupt", func() bool { return len(responded(f)) == 3 })
	for i, want := range []string{`"uuid":"id2"`, `"message_uuid":"id2"`, `"subtype":"interrupt"`} {
		if got := responded(f)[i]; !strings.Contains(got, want) {
			t.Errorf("%d: no %s in %s", i, want, got)
		}
	}
}

// Withdrawing one queued prompt ($/cancel_request) drops it alone: the
// running prompt and those queued after it go on.
func TestWithdrawQueuedPrompt(t *testing.T) {
	g := gates(4)
	f := fake(user("id2"), signal(g[0]), echo("id2"), user("id3"), signal(g[1]), user("id4"), signal(g[2]),
		claudetest.Until(g[3]),
		result(names("id2")), claudetest.Send(idle),
		echo("id4"), result(names("id4")), claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	one := p.prompt(t, sid, "one")
	wait(t, g[0], "one read")
	ctx, withdraw := context.WithCancel(t.Context())
	two, err := p.StartPrompt(ctx, &acp.PromptRequest{SessionID: sid, Prompt: steerText("two")})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, g[1], "two read")
	three := p.prompt(t, sid, "three")
	wait(t, g[2], "three read")
	withdraw()
	if _, err := two(ctx); err == nil { // and the request is withdrawn
		t.Error("two answered")
	}
	eventually(t, "two dropped", func() bool { ids, _ := p.queue(t, sid); return !slices.Contains(ids, "id3") })
	close(g[3])
	if got := stopReason(one); got != "end_turn" {
		t.Errorf("one: %s", got)
	}
	if got := stopReason(three); got != "end_turn" {
		t.Errorf("three: %s", got)
	}
	if got := asked(f); len(got) != 1 || !strings.Contains(got[0], `"message_uuid":"id3"`) {
		t.Errorf("claude was asked %q", got)
	}
}

// A cancel drops the running turn's steers claude hasn't taken, before
// the interrupt: claude would run them after it, as a turn of their own.
func TestCancelDropsSteers(t *testing.T) {
	steered, release := make(chan struct{}), make(chan struct{})
	f := fake(user("id2"), echo("id2"),
		claudetest.Expect(`{"type":"user","uuid":"id3","priority":"now"}`), signal(steered),
		claudetest.Until(release), result(names("id2")), claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	one := p.prompt(t, sid, "one")
	eventually(t, "one running", func() bool { _, a := p.queue(t, sid); return a == "id2" })
	if outcome, err := steering.Steer(t.Context(), p.AgentConn, string(sid), steerText("also")); err != nil || outcome != steering.Injected {
		t.Fatalf("%s %v", outcome, err)
	}
	wait(t, steered, "the steer read")
	if err := p.Cancel(t.Context(), &acp.CancelNotification{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a drop and an interrupt", func() bool { return len(asked(f)) == 2 })
	for i, want := range []string{`"message_uuid":"id3"`, `"subtype":"interrupt"`} {
		if got := asked(f)[i]; !strings.Contains(got, want) {
			t.Errorf("%d: no %s in %s", i, want, got)
		}
	}
	close(release)
	if got := stopReason(one); got != "cancelled" {
		t.Errorf("one: %s", got)
	}
}
