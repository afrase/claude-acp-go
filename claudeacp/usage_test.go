package claudeacp

import (
	"errors"
	"slices"
	"strings"
	"testing"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// A failed turn's errorKind is its own API error's, not one an earlier
// turn's message named.
func TestErrorKindPerTurn(t *testing.T) {
	g := gates(1)
	f := fake(user("id2"), echo("id2"),
		sendf(`{"type":"assistant","error":"overloaded","message":{"id":"m1","model":"<synthetic>","content":[{"type":"text","text":"API Error: overloaded"}]}}`),
		sendf(`{"type":"result","subtype":"success","is_error":true,"result":"API Error: overloaded","user_message_uuids":["id2"]}`),
		claudetest.Send(idle), signal(g[0]),
		user("id3"), echo("id3"),
		sendf(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["it broke"],"user_message_uuids":["id3"]}`),
		claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	kind := func(text string) string {
		_, err := p.prompt(t, sid, text)()
		var e *acp.Error
		if !errors.As(err, &e) {
			t.Fatalf("%s: %v", text, err)
		}
		return string(e.Data)
	}
	if got := kind("one"); got != `{"errorKind":"overloaded"}` {
		t.Errorf("one: %s", got)
	}
	wait(t, g[0], "one's idle")
	if got := kind("two"); got != "" {
		t.Errorf("two: %s", got)
	}
}

// claude's own message ("<synthetic>": an API error) isn't the session's
// model: the usage names the model that ran, and the window is its.
func TestSyntheticModel(t *testing.T) {
	f := fake(user("id2"), echo("id2"),
		event(`{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":1000}}}`),
		sendf(`{"type":"assistant","error":"server_error","message":{"id":"m2","model":"<synthetic>","content":[{"type":"text","text":"API Error: 500"}]}}`),
		result(names("id2")+`,"modelUsage":{"claude-opus-5-5":{"contextWindow":200000},"claude-haiku-4-5":{"contextWindow":1000000}}`),
		claudetest.Send(idle))
	p := start(t, nil, f)
	sid := p.open(t)
	_ = stopReason(p.prompt(t, sid, "go"))
	us := p.of("usage_update")
	if len(us) == 0 {
		t.Fatal("no usage")
	}
	last := us[len(us)-1].UsageUpdate
	if last.Size != 200000 || last.Meta["_claude/model"] != "claude-opus-5-5" {
		t.Errorf("size %d, model %v", last.Size, last.Meta["_claude/model"])
	}
}

// A rate limit's news before any message has counted the context sends
// no usage, which would show the session at zero; after, it goes with
// the context's use.
func TestRateLimitUsage(t *testing.T) {
	limit := sendf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`)
	f := fake(user("id2"), limit, echo("id2"),
		event(`{"type":"message_start","message":{"id":"msg_1","model":"claude-x","usage":{"input_tokens":10}}}`),
		limit, result(names("id2")), claudetest.Send(idle))
	p := start(t, nil, f)
	_ = stopReason(p.prompt(t, p.open(t), "go"))
	var got []string
	for _, u := range p.of("usage_update") {
		line := "used"
		if _, ok := u.UsageUpdate.Meta["_claude/rateLimit"]; ok {
			line = "limit"
		}
		if u.UsageUpdate.Used == 0 {
			line += " at zero"
		}
		got = append(got, line)
	}
	if want := []string{"used", "limit", "used"}; !slices.Equal(got, want) {
		t.Errorf("%s", strings.Join(got, ", "))
	}
}
