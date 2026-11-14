package claudetest

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/afrase/claude-acp-go/claudeagent"
)

const cassette = `{"argv":["--output-format","stream-json","--session-id=rec-s"],"envKeys":["PATH"],"cli":"2.1.312"}
{"dir":"in","ms":0,"frame":{"type":"control_request","request_id":"rec-r","request":{"subtype":"initialize","hooks":{}}}}
{"dir":"out","ms":5,"frame":{"type":"control_response","response":{"subtype":"success","request_id":"rec-r","response":{}}}}
{"dir":"in","ms":6,"frame":{"type":"keep_alive"}}

{"dir":"in","ms":7,"frame":{"type":"user","uuid":"rec-u","session_id":"","message":{"role":"user","content":"hi"}}}
{"dir":"out","ms":9,"frame":{"type":"user","uuid":"rec-u","session_id":"rec-s","isReplay":true}}
{"dir":"out","ms":20,"frame":{"type":"result","subtype":"success","session_id":"rec-s","user_message_uuids":["rec-u"],"result":"rec-u was here"}}
`

// A replay expects what the client writes and writes what claude wrote,
// with the client's ids for the recorded session, request and message;
// an id inside other text stays.
func TestReplay(t *testing.T) {
	c, err := ReadCassette(strings.NewReader(cassette))
	if err != nil {
		t.Fatal(err)
	}
	if c.Header.CLI != "2.1.312" || len(c.Frames) != 6 {
		t.Fatalf("%+v", c)
	}
	f := Replay(c)
	p, err := f.Spawn(context.Background(), claudeagent.Spec{Path: "/claude", Args: []string{"--session-id", "live-s"}})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, p.Stderr) }()
	t.Cleanup(func() {
		_ = p.Signal(os.Kill)
		<-f.Exited()
	})
	s := started{Proc: p, lines: bufio.NewScanner(p.Stdout)}
	s.write(t, `{"type":"control_request","request_id":"req_1","request":{"subtype":"initialize"}}`)
	if got := s.read(t); got != `{"type":"control_response","response":{"subtype":"success","request_id":"req_1","response":{}}}` {
		t.Errorf("%s", got)
	}
	s.write(t, `{"type":"user","uuid":"u-live","message":{"role":"user","content":"hi"}}`)
	for _, want := range []string{
		`{"type":"user","uuid":"u-live","session_id":"live-s","isReplay":true}`,
		`{"type":"result","subtype":"success","session_id":"live-s","user_message_uuids":["u-live"],"result":"rec-u was here"}`,
	} {
		if got := s.read(t); got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
	<-f.Played()
	_ = p.Stdin.Close()
	if err := p.Wait(); err != nil || f.Err() != nil {
		t.Errorf("exit %v, script %v", err, f.Err())
	}
}

// A line that isn't what was recorded fails the replay.
func TestReplayMismatch(t *testing.T) {
	c, err := ReadCassette(strings.NewReader(cassette))
	if err != nil {
		t.Fatal(err)
	}
	f := Replay(c)
	s := start(t, f)
	s.write(t, `{"type":"user","uuid":"u","message":{"role":"user","content":"too soon"}}`)
	if err := s.Wait(); err == nil || f.Err() == nil || !strings.Contains(f.Err().Error(), "too soon") {
		t.Errorf("exit %v, script %v", err, f.Err())
	}
}

const decisions = `{"argv":["--session-id","rec-s"],"envKeys":[],"cli":"2.1.312"}
{"dir":"in","ms":0,"frame":{"type":"control_request","request_id":"rec-init","request":{"subtype":"initialize","hooks":{"Stop":[{"hookCallbackIds":["hook_0"]}]}}}}
{"dir":"out","ms":1,"frame":{"type":"control_response","response":{"subtype":"success","request_id":"rec-init","response":{}}}}
{"dir":"out","ms":2,"frame":{"type":"control_request","request_id":"c1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}}
{"dir":"in","ms":3,"frame":{"type":"control_response","response":{"subtype":"success","request_id":"c1","response":{"behavior":"allow","toolUseID":"t1","updatedInput":{"command":"ls"}}}}}
{"dir":"in","ms":4,"frame":{"type":"user","uuid":"rec-u","session_id":"rec-s","timestamp":"2026-11-01T00:00:00Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}}
{"dir":"in","ms":5,"frame":{"type":"control_request","request_id":"rec-c","request":{"subtype":"cancel_async_message","message_uuid":"rec-u"}}}
{"dir":"in","ms":6,"frame":{"type":"control_request","request_id":"rec-stop","request":{"subtype":"stop_task","task_id":"task-1"}}}
`

// A replay checks what the client decided: a request's arguments, an
// answer's outcome and the request it answers, a message's content. The
// ids it gave, the time and the client's own setup can differ.
func TestReplayChecksDecisions(t *testing.T) {
	init := `{"type":"control_request","request_id":"live-init","request":{"subtype":"initialize","hooks":{}}}`
	answer := `{"type":"control_response","response":{"subtype":"success","request_id":"c1","response":{"behavior":"allow","toolUseID":"t1","updatedInput":{"command":"ls -a"}}}}`
	user := `{"type":"user","uuid":"live-u","session_id":"live-s","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`
	cancel := `{"type":"control_request","request_id":"live-c","request":{"subtype":"cancel_async_message","message_uuid":"live-u"}}`
	stop := `{"type":"control_request","request_id":"live-stop","request":{"subtype":"stop_task","task_id":"task-1"}}`
	for name, tc := range map[string]struct {
		old, new string // what in the lines above the client writes instead
		fails    bool
	}{
		"as recorded":      {},
		"denied":           {old: `"behavior":"allow"`, new: `"behavior":"deny"`, fails: true},
		"an error":         {old: `{"subtype":"success","request_id":"c1"`, new: `{"subtype":"error","request_id":"c1"`, fails: true},
		"another request":  {old: `"request_id":"c1"`, new: `"request_id":"c2"`, fails: true},
		"other text":       {old: `"text":"hi"`, new: `"text":"bye"`, fails: true},
		"another message":  {old: `"message_uuid":"live-u"`, new: `"message_uuid":"rec-u"`, fails: true},
		"another task":     {old: `"task_id":"task-1"`, new: `"task_id":"task-2"`, fails: true},
		"a field added":    {old: `"task_id":"task-1"`, new: `"task_id":"task-1","why":"asked"`},
		"no session id":    {old: `"session_id":"live-s",`, new: ``},
		"setup of its own": {old: `"hooks":{}`, new: `"hooks":{"Stop":[{"hookCallbackIds":["h"]}]},"agents":{}`},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := ReadCassette(strings.NewReader(decisions))
			if err != nil {
				t.Fatal(err)
			}
			f := Replay(c)
			p, err := f.Spawn(context.Background(), claudeagent.Spec{Path: "/claude", Args: []string{"--session-id", "live-s"}})
			if err != nil {
				t.Fatal(err)
			}
			go func() { _, _ = io.Copy(io.Discard, p.Stderr) }()
			go func() { _, _ = io.Copy(io.Discard, p.Stdout) }()
			for _, line := range []string{init, answer, user, cancel, stop} {
				if tc.old != "" {
					line = strings.Replace(line, tc.old, tc.new, 1)
				}
				_, _ = io.WriteString(p.Stdin, line+"\n") // fails once the fake has ended
			}
			_ = p.Stdin.Close()
			err = p.Wait()
			if failed := f.Err() != nil; failed != tc.fails || (err != nil) != tc.fails {
				t.Errorf("exit %v, script %v; want it to fail: %v", err, f.Err(), tc.fails)
			}
		})
	}
}

func TestReadCassetteRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"empty":     "",
		"no header": `{"dir":"in","frame":{"type":"user"}}`,
		"bad dir":   `{"argv":[]}` + "\n" + `{"dir":"up","frame":{"type":"user"}}`,
		"no frame":  `{"argv":[]}` + "\n" + `{"dir":"in"}`,
	} {
		if _, err := ReadCassette(strings.NewReader(text)); err == nil {
			t.Errorf("%s: read", name)
		}
	}
}
