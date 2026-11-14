package claudeacp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
	"github.com/afrase/claude-acp-go/claudeagent/sessions"
)

// titles are the titles of the session_info_updates so far.
func (c *client) titles() []string {
	var out []string
	for _, u := range c.of("session_info_update") {
		if v := u.SessionInfoUpdate.Title.Value; v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// twoTurns are the turns of prompts a and b, each answered "Sure." and
// ended; done closes once claude has played them.
func twoTurns(a, b string, done chan struct{}) []claudetest.Step {
	turn := func(id string) []claudetest.Step {
		return []claudetest.Step{user(id), echo(id), assistant(`{"type":"text","text":"Sure."}`), result(names(id)), claudetest.Send(idle)}
	}
	return append(append(turn(a), turn(b)...), signal(done))
}

// nthUUID is the nth of a run of UUIDs.
func nthUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// A session is titled once: claude makes the title from the conversation
// after the first turn and keeps it; nothing asks again. A title that
// can't be made sends nothing.
func TestTitle(t *testing.T) {
	for _, tc := range []struct{ title, want string }{{`"Fixing the login page"`, "Fixing the login page"}, {`null`, ""}} {
		t.Run(tc.want, func(t *testing.T) {
			done := make(chan struct{})
			f := started(`{}`, tc.title, twoTurns("id2", "id3", done)...)
			p := start(t, nil, f)
			sid := p.open(t)
			stopReason(p.prompt(t, sid, "Fix the login page"))
			eventually(t, "the ask", func() bool { return len(responded(f)) == 1 })
			stopReason(p.prompt(t, sid, "and the logout one"))
			wait(t, done, "the script")
			if _, err := p.CloseSession(context.Background(), &acp.CloseSessionRequest{SessionID: sid}); err != nil {
				t.Fatal(err)
			}
			var want []string
			if tc.want != "" {
				want = []string{tc.want}
			}
			if got := p.titles(); !slices.Equal(got, want) {
				t.Errorf("titles %q", got)
			}
			if got := responded(f); len(got) != 1 {
				t.Fatalf("asked %q", got)
			}
			var ask struct {
				Request struct {
					Description string `json:"description"`
					Persist     bool   `json:"persist"`
				} `json:"request"`
			}
			if err := json.Unmarshal([]byte(responded(f)[0]), &ask); err != nil {
				t.Fatal(err)
			}
			if ask.Request.Description != "Fix the login page\nSure.\n" || !ask.Request.Persist {
				t.Errorf("%+v", ask.Request)
			}
		})
	}
}

// A title in the session's transcript (a /rename, one claude kept) is
// the session's, and claude isn't asked for one.
func TestTitleFromTranscript(t *testing.T) {
	done := make(chan struct{})
	f := started(`{}`, `"Never asked"`, twoTurns(nthUUID(2), nthUUID(3), done)...)
	p := start(t, nil, f)
	var n atomic.Int32 // the transcript's name is a UUID
	p.agent.newID = func() string { return nthUUID(int(n.Add(1))) }
	dir := t.TempDir()
	r, err := p.NewSession(context.Background(), &acp.NewSessionRequest{Cwd: dir, MCPServers: []acp.MCPServer{}})
	if err != nil {
		t.Fatal(err)
	}
	project := sessions.Store{ConfigDir: p.agent.cfg.ConfigDir}.ProjectDir(dir)
	transcript := `{"type":"user","uuid":"u1","message":{"role":"user","content":"Fix the login page"}}` + "\n" +
		`{"type":"custom-title","customTitle":"Login"}` + "\n"
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, string(r.SessionID)+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	stopReason(p.prompt(t, r.SessionID, "Fix the login page"))
	eventually(t, "the title", func() bool { return len(p.titles()) == 1 })
	stopReason(p.prompt(t, r.SessionID, "and the logout one"))
	wait(t, done, "the script")
	if _, err := p.CloseSession(context.Background(), &acp.CloseSessionRequest{SessionID: r.SessionID}); err != nil {
		t.Fatal(err)
	}
	if got := p.titles(); !slices.Equal(got, []string{"Login"}) {
		t.Errorf("titles %q", got)
	}
	if got := responded(f); len(got) != 0 {
		t.Errorf("asked %q", got)
	}
}

// A reopened session with no title is titled from its conversation, the
// history included, not from the first prompt after the load alone.
func TestTitleAfterResume(t *testing.T) {
	done := make(chan struct{})
	f := started(`{}`, `"Fixing the login page"`, user("id1"), echo("id1"), assistant(`{"type":"text","text":"Running them."}`),
		result(names("id1")), claudetest.Send(idle), signal(done))
	p := start(t, nil, f)
	cwd, sid := t.TempDir(), nthUUID(1)
	p.writeHistory(t, cwd, sid, userRec("u1", "Fix the login page"), asstRec("a1", "msg_1", textBlock("Fixed it.")),
		userRec("u2", "<command-name>/cost</command-name>"))
	if _, err := p.ResumeSession(context.Background(), &acp.ResumeSessionRequest{SessionID: acp.SessionID(sid), Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	stopReason(p.prompt(t, acp.SessionID(sid), "please run the tests again"))
	wait(t, done, "the script")
	eventually(t, "the ask", func() bool { return len(responded(f)) == 1 })
	var ask struct {
		Request struct{ Description string } `json:"request"`
	}
	if err := json.Unmarshal([]byte(responded(f)[0]), &ask); err != nil {
		t.Fatal(err)
	}
	if want := "Fix the login page\nFixed it.\nplease run the tests again\nRunning them.\n"; ask.Request.Description != want {
		t.Errorf("made from %q, want %q", ask.Request.Description, want)
	}
}
