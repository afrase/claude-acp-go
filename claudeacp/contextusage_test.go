package claudeacp

import (
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// usageAnswer is the fake's answer to get_context_usage: the fields
// claudeagent.ContextUsage decodes, and one it doesn't, which stands for
// whatever else claude puts in an answer.
const usageAnswer = `{"categories":[{"name":"Messages","tokens":5,"kind":"used"},{"name":"Free space","tokens":995,"kind":"free"}],` +
	`"totalTokens":5,"maxTokens":1000,"rawMaxTokens":1000,"percentage":0.5,"model":"claude-x","laterField":[{"name":"x","tokens":1}]}`

// usageAsks are the get_context_usage requests f answered but for the
// session's own ask for its window (detail "summary", as it starts):
// each request's fields but its subtype, as JSON.
func usageAsks(t *testing.T, f *claudetest.Fake) []string {
	t.Helper()
	var out []string
	for _, l := range f.Responded() {
		if !strings.Contains(l, `"get_context_usage"`) {
			continue
		}
		var ask struct {
			Request map[string]any `json:"request"`
		}
		if err := json.Unmarshal([]byte(l), &ask); err != nil {
			t.Fatal(err)
		}
		delete(ask.Request, "subtype")
		b, _ := json.Marshal(ask.Request)
		out = append(out, string(b))
	}
	return out
}

// The context's usage is claude's answer, whole, while a turn runs: what
// claudeagent types and what it doesn't reach the client as claude sent
// them, the detail goes to claude as asked (none for claude's default),
// and the turn goes on.
func TestContextUsage(t *testing.T) {
	taken, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f := fake(user("id2"), echo("id2"), signal(taken), claudetest.Do(func() { <-release }),
		assistant(`{"type":"text","text":"Sure."}`), result(names("id2")), claudetest.Send(idle), signal(done))
	f.Respond(`{"type":"control_request","request":{"subtype":"get_context_usage"}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"$req","response":`+usageAnswer+`}}`)
	p, sid := startOpen(t, nil, f)
	w := p.prompt(t, sid, "go")
	wait(t, taken, "the prompt taken")

	for _, detail := range []string{"full", ""} {
		var got json.RawMessage
		if err := p.Call(t.Context(), ContextUsageMethod, ContextUsageRequest{SessionID: string(sid), Detail: detail}, &got); err != nil {
			t.Fatal(err)
		}
		if keysSorted(got) != keysSorted([]byte(usageAnswer)) {
			t.Errorf("detail %q: %s", detail, got)
		}
	}
	sameLines(t, usageAsks(t, f), []string{`{"detail":"summary"}`, `{"detail":"full"}`, `{}`})

	close(release)
	stopsWith(t, w, string(acp.StopReasonEndTurn))
	wait(t, done, "the script")
}

// An answer with nothing in it is an empty object; claude's error is the
// call's; a session that isn't open is refused.
func TestContextUsageNone(t *testing.T) {
	f := fake()
	f.Respond(`{"type":"control_request","request":{"subtype":"get_context_usage","detail":"full"}}`,
		`{"type":"control_response","response":{"subtype":"error","request_id":"$req","error":"no session yet"}}`)
	f.Respond(`{"type":"control_request","request":{"subtype":"get_context_usage"}}`,
		`{"type":"control_response","response":{"subtype":"success","request_id":"$req"}}`)
	p, sid := startOpen(t, nil, f)
	var got json.RawMessage
	if err := p.Call(t.Context(), ContextUsageMethod, ContextUsageRequest{SessionID: string(sid), Detail: "summary"}, &got); err != nil || string(got) != `{}` {
		t.Fatalf("%s %v", got, err)
	}
	err := p.Call(t.Context(), ContextUsageMethod, ContextUsageRequest{SessionID: string(sid), Detail: "full"}, &got)
	if err == nil || !strings.Contains(err.Error(), "no session yet") {
		t.Errorf("claude's error: %v", err)
	}
	if err := p.Call(t.Context(), ContextUsageMethod, ContextUsageRequest{SessionID: "nope"}, &got); err == nil {
		t.Error("a session that isn't open answered")
	}
}
