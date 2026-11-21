package claudeacp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/afrase/claude-acp-go/claudeagent/claudetest"
)

// Image blocks, as claude writes them: one with its data, one by URL and
// one with neither.
const (
	pngBlock   = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}`
	urlBlock   = `{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}`
	emptyBlock = `{"type":"image","source":{"type":"base64"}}`
)

// jsonMap is the JSON object s.
func jsonMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// images are the image chunks of kind so far, as messageId:mimeType:data:uri.
func (c *client) images(kind string) []string {
	var out []string
	for _, u := range c.of(kind) {
		ch := u.AgentMessageChunk
		if kind == "user_message_chunk" {
			ch = u.UserMessageChunk
		}
		if img := ch.Content.Image; img != nil {
			out = append(out, strings.Join([]string{string(ch.MessageID), img.MimeType, img.Data, img.URI}, ":"))
		}
	}
	return out
}

// An image in the model's message streams as an image chunk under the
// message's id: with its data and type, or with its URL and neither, as
// claude-agent-acp sends one. One with neither is left out.
func TestStreamImage(t *testing.T) {
	f := fake(user("id2"), echo("id2"),
		claudetest.Send(`{"type":"assistant","message":{"id":"msg_1","model":"claude-x","content":[`+pngBlock+`,`+urlBlock+`,`+emptyBlock+`]}}`),
		result(names("id2")), claudetest.Send(idle))
	p, sid := startOpen(t, nil, f)
	stopsWith(t, p.prompt(t, sid, "draw"), "end_turn")
	want := []string{"msg_1:image/png:iVBORw0KGgo=:", "msg_1:::https://example.com/a.png"}
	if got := p.images("agent_message_chunk"); !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// A load replays the images in the history: the user's (pasted, say)
// under its message's uuid, the model's under the API's id, the same way
// the live stream sends them.
func TestLoadReplaysImages(t *testing.T) {
	p := start(t, nil, fake())
	cwd, sid := t.TempDir(), nthUUID(1)
	block := func(s string) map[string]any { return jsonMap(t, s) }
	p.writeHistory(t, cwd, sid,
		userRec("u1", []any{textBlock("What is this?"), block(pngBlock), block(emptyBlock)}),
		asstRec("a1", "msg_1", block(urlBlock)),
		asstRec("a2", "msg_1", textBlock("A picture.")),
	)
	p.mustLoad(t, sid, cwd)
	if got := p.images("user_message_chunk"); !slices.Equal(got, []string{"u1:image/png:iVBORw0KGgo=:"}) {
		t.Errorf("the user's: %q", got)
	}
	if got := p.images("agent_message_chunk"); !slices.Equal(got, []string{"msg_1:::https://example.com/a.png"}) {
		t.Errorf("the model's: %q", got)
	}
	want := []string{sid + " user u1 What is this?", sid + " user u1 image", sid + " text msg_1 image", sid + " text msg_1 A picture.", sid + " commands"}
	if got := p.replayed(); !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A subagent's images replay on its own session, not the user's.
func TestLoadReplaysSubagentImage(t *testing.T) {
	p := start(t, withSubagents, fake())
	cwd, sid := t.TempDir(), nthUUID(1)
	p.writeHistory(t, cwd, sid,
		userRec("u1", "Look"),
		asstRec("a1", "msg_1", useBlock("toolu_A", "Agent", `{"description":"Look","prompt":"look"}`)),
		userRec("r1", resultBlocks("toolu_A", "seen", false)),
	)
	p.writeSubagent(t, cwd, sid, "agentA", "toolu_A",
		userRec("s1", []any{textBlock("look"), jsonMap(t, pngBlock)}),
		asstRec("s2", "msg_A", jsonMap(t, urlBlock)),
	)
	p.mustLoad(t, sid, cwd)
	var got []string
	for _, n := range p.notes() {
		if ch := n.Update.AgentMessageChunk; ch != nil && ch.Content.Image != nil {
			got = append(got, string(n.SessionID)+" "+string(ch.MessageID)+" "+ch.Content.Image.URI)
		}
		if ch := n.Update.UserMessageChunk; ch != nil && ch.Content.Image != nil {
			got = append(got, string(n.SessionID)+" user "+string(ch.MessageID))
		}
	}
	if !slices.Equal(got, []string{"agentA msg_A https://example.com/a.png"}) {
		t.Errorf("%q", got)
	}
}
