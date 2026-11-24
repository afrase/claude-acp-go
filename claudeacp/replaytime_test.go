package claudeacp

import (
	"testing"

	acp "github.com/afrase/acp-go/unstable"
)

// A replayed message's first chunk says when the message was sent, its
// record's timestamp as claude wrote it, under _meta.claudeCode.timestamp;
// an agent message written a record a block says its first record's,
// thought or text. A message whose record has no timestamp says none,
// and a chunk after the first says nothing.
func TestLoadReplaysTimestamps(t *testing.T) {
	p := start(t, nil, fake())
	cwd, sid := t.TempDir(), nthUUID(1)
	think := map[string]any{"type": "thinking", "thinking": "Hmm.", "signature": "x"}
	p.writeHistory(t, cwd, sid,
		userRec("u1", []any{textBlock("Hi"), textBlock("there")}, "timestamp", "2026-11-16T03:07:16.039Z"),
		withField(asstRec("a1", "msg_1", think), "timestamp", "2026-11-16T03:07:16.059Z"),
		withField(asstRec("a2", "msg_1", textBlock("Hello.")), "timestamp", "2026-11-16T03:07:17.000Z"),
		userRec("u2", "Bye"),
		asstRec("a3", "msg_2", textBlock("Bye.")),
	)
	p.mustLoad(t, sid, cwd)
	var got []string
	for _, n := range p.notes() {
		u := n.Update
		var c *acp.ContentChunk
		switch {
		case u.UserMessageChunk != nil, u.AgentThoughtChunk != nil, u.AgentMessageChunk != nil:
			c = chunkOf(u)
		default:
			continue
		}
		line := u.SessionUpdate + " " + string(c.MessageID)
		if ts := str(claudeMeta(c.Meta)["timestamp"]); ts != "" {
			line += " " + ts
		}
		got = append(got, line)
	}
	sameLines(t, got, []string{
		"user_message_chunk u1 2026-11-16T03:07:16.039Z",
		"user_message_chunk u1",
		"agent_thought_chunk msg_1 2026-11-16T03:07:16.059Z",
		"agent_message_chunk msg_1",
		"user_message_chunk u2",
		"agent_message_chunk msg_2",
	})
}

// withField is r with the field name set to v.
func withField(r rec, name string, v any) rec {
	r[name] = v
	return r
}

// A subagent's replayed message keeps naming its call next to its time:
// the first chunk's _meta.claudeCode has both parentToolUseId and
// timestamp. (A client without subagents gets none of a subagent's
// steps on a load.)
func TestLoadReplaysSubagentTimestamps(t *testing.T) {
	p := start(t, withSubagents, fake())
	cwd, sid := t.TempDir(), nthUUID(1)
	p.writeHistory(t, cwd, sid,
		userRec("u1", "Count", "timestamp", "2026-11-16T03:07:16.039Z"),
		withField(asstRec("a1", "msg_1", useBlock("toolu_A", "Agent", `{"description":"Count","prompt":"count"}`)),
			"timestamp", "2026-11-16T03:07:16.059Z"),
		userRec("r1", resultBlocks("toolu_A", "2", false), "timestamp", "2026-11-16T03:07:19.000Z"),
	)
	p.writeSubagent(t, cwd, sid, "agentA", "toolu_A",
		withField(asstRec("s1", "msg_A1", textBlock("1, 2")), "timestamp", "2026-11-16T03:07:18.000Z"),
		withField(asstRec("s2", "msg_A1", textBlock("3")), "timestamp", "2026-11-16T03:07:18.500Z"))
	p.mustLoad(t, sid, cwd)
	var got []string
	for _, n := range p.notes() {
		u := n.Update
		if u.AgentMessageChunk == nil {
			continue
		}
		m := claudeMeta(chunkOf(u).Meta)
		got = append(got, str(m["parentToolUseId"])+" "+str(m["timestamp"]))
	}
	sameLines(t, got, []string{
		"toolu_A 2026-11-16T03:07:18.000Z",
		"toolu_A ",
	})
}
