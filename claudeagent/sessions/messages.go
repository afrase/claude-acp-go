// Ported from the Claude Agent SDK for Python (commit 27858cbb,
// src/claude_agent_sdk/_internal/sessions.py), MIT License,
// Copyright (c) 2025 Anthropic, PBC: see ../NOTICE. Modified: restructured
// as Go, the functions a Store's methods and records read into typed
// structs.

package sessions

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Message is one user or assistant message of a transcript.
type Message struct {
	// Type is "user" or "assistant".
	Type string
	// UUID is the record's uuid in the transcript.
	UUID string
	// SessionID is the session the record was written in.
	SessionID string
	// Message is the message as the Messages API has it: its role and
	// content, and an assistant message's id, model and usage. The CLI
	// writes an assistant response as one record per content block, all
	// with the same id.
	Message json.RawMessage
	// ParentToolUseID is, for a subagent's message, the id of the Agent
	// tool call that started the subagent; "" when unknown.
	ParentToolUseID string
	// ParentAgentID is, for a nested subagent's message, the id of the
	// subagent that started this one.
	ParentAgentID string
	// Raw is the whole record, for the fields not named here (timestamp,
	// isCompactSummary, toolUseResult and so on).
	Raw json.RawMessage
}

// Messages returns session id's conversation as it stands, oldest first:
// the user and assistant messages on the chain of parentUuid links from the
// latest message back, with every tool call and result of a turn that ran
// tools in parallel. After a compaction the chain starts at its summary
// (a user message with isCompactSummary). Meta messages and subagents'
// are left out. An error matching fs.ErrNotExist says the session isn't
// there.
func (s Store) Messages(id, cwd string) ([]Message, error) {
	t, err := s.open(id, cwd)
	if err != nil {
		return nil, err
	}
	return t.Messages()
}

// Messages returns the session's conversation as it stands, as
// Store.Messages does.
func (t *Transcript) Messages() ([]Message, error) {
	data, err := os.ReadFile(t.path)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, e := range chain(parseEntries(data)) {
		if (e.typ == "user" || e.typ == "assistant") && !e.meta && !e.sidechain && !e.team {
			out = append(out, e.toMessage())
		}
	}
	return out, nil
}

func (e *entry) toMessage() Message {
	return Message{Type: e.typ, UUID: e.uuid, SessionID: e.sessionID, Message: e.message, Raw: e.raw}
}

// Subagent is a subagent of a session: its transcript, and the details its
// sidecar file (agent-<id>.meta.json) keeps.
type Subagent struct {
	// ID is the subagent's id: its transcript is agent-<ID>.jsonl.
	ID string
	// ParentToolUseID is the id of the Agent tool call that started it.
	ParentToolUseID string
	// ParentAgentID is the subagent that started this one, if one did.
	ParentAgentID string
	// WorktreePath is the worktree claude made for it (the Agent tool's
	// isolation), while that worktree stands.
	WorktreePath string

	path string
}

// Subagents returns the subagents of session id that have a transcript,
// in the order of their files' names. A session with none has none; an
// error matching fs.ErrNotExist says the session isn't there.
func (s Store) Subagents(id, cwd string) ([]Subagent, error) {
	t, err := s.open(id, cwd)
	if err != nil {
		return nil, err
	}
	return t.Subagents(), nil
}

// Subagents returns the session's subagents that have a transcript, as
// Store.Subagents does. Each one's Messages and QueuedPrompts read its
// transcript without looking for it again.
func (t *Transcript) Subagents() []Subagent {
	var out []Subagent
	walkAgents(filepath.Join(strings.TrimSuffix(t.path, ".jsonl"), "subagents"), &out)
	return out
}

// walkAgents collects the agent-<id>.jsonl files in dir and the folders
// below it (a workflow's subagents sit in subagents/workflows/<run>/),
// reading each one's sidecar. Links aren't followed.
func walkAgents(dir string, out *[]Subagent) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			walkAgents(path, out)
			continue
		}
		name, ok := strings.CutPrefix(e.Name(), "agent-")
		if name, ok2 := strings.CutSuffix(name, ".jsonl"); ok && ok2 && e.Type().IsRegular() {
			*out = append(*out, withMeta(Subagent{ID: name, path: path}))
		}
	}
}

// maxMeta is the most of a sidecar read: claude's are a few hundred bytes.
const maxMeta = 1 << 16

// withMeta is a with what the sidecar beside its transcript says; one
// that is missing or isn't a JSON object tells nothing, nor does one that
// isn't a regular file: the folder is the agent's to write, and a pipe
// there would hold the read for good.
func withMeta(a Subagent) Subagent {
	path := strings.TrimSuffix(a.path, ".jsonl") + ".meta.json"
	f, err := openRegular(path)
	if err != nil {
		return a
	}
	defer f.Close() //nolint:errcheck // read-only
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return a
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMeta))
	if err != nil {
		return a
	}
	var meta struct {
		ToolUseID     str `json:"toolUseId"`
		ParentAgentID str `json:"parentAgentId"`
		WorktreePath  str `json:"worktreePath"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return a
	}
	a.ParentToolUseID, a.ParentAgentID, a.WorktreePath = string(meta.ToolUseID), string(meta.ParentAgentID), string(meta.WorktreePath)
	return a
}

// SubagentMessages returns the messages of session id's subagent agentID,
// oldest first, each with the Agent tool call that started the subagent.
// An error matching fs.ErrNotExist says the session or the subagent isn't
// there.
func (s Store) SubagentMessages(id, cwd, agentID string) ([]Message, error) {
	a, err := s.subagent(id, cwd, agentID)
	if err != nil {
		return nil, err
	}
	return a.Messages()
}

// Messages returns the subagent's messages, oldest first, as
// Store.SubagentMessages does. A Subagent that isn't Subagents' has none
// (an error matching fs.ErrNotExist).
func (a Subagent) Messages() ([]Message, error) {
	if a.path == "" {
		return nil, notFound("subagent " + a.ID)
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		return nil, err
	}
	var out []Message
	for _, e := range linear(parseEntries(data)) {
		if e.typ == "user" || e.typ == "assistant" {
			m := e.toMessage()
			m.ParentToolUseID, m.ParentAgentID = a.ParentToolUseID, a.ParentAgentID
			out = append(out, m)
		}
	}
	return out, nil
}

// subagent is session id's subagent agentID. It is found among the
// session's files, so an id never names a path.
func (s Store) subagent(id, cwd, agentID string) (Subagent, error) {
	agents, err := s.Subagents(id, cwd)
	if err != nil {
		return Subagent{}, err
	}
	i := slices.IndexFunc(agents, func(a Subagent) bool { return a.ID == agentID })
	if agentID == "" || i < 0 {
		return Subagent{}, notFound("subagent " + agentID + " of session " + id)
	}
	return agents[i], nil
}
