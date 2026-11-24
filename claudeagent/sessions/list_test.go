// Partly ported from the Claude Agent SDK for Python's tests (commit
// 27858cbb, tests/test_sessions.py), MIT License, Copyright (c) 2025
// Anthropic, PBC: see ../NOTICE.

package sessions

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture is a transcript as test_sessions.py's _make_session_file writes
// one: a prompt, an answer, and a record of details at the end.
type fixture struct {
	prompt, summary, title, branch, cwd string
	sidechain                           bool
	modified                            time.Time
	extra                               []string // lines after the rest
}

// write writes the fixture as session id's transcript in folder dir.
func (f fixture) write(t *testing.T, dir, id string) {
	t.Helper()
	line := func(v map[string]any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": cmp.Or(f.prompt, "Hello Claude")}}
	tail := map[string]any{"type": "summary"}
	for k, v := range map[string]string{"cwd": f.cwd, "gitBranch": f.branch} {
		if v != "" {
			first[k] = v
		}
	}
	if f.sidechain {
		first["isSidechain"] = true
	}
	for k, v := range map[string]string{"summary": f.summary, "customTitle": f.title, "gitBranch": f.branch} {
		if v != "" {
			tail[k] = v
		}
	}
	lines := append([]string{line(first), `{"type":"assistant","message":{"role":"assistant","content":"Hi there!"}}`, line(tail)}, f.extra...)
	writeLines(t, dir, id, lines...)
	if !f.modified.IsZero() {
		if err := os.Chtimes(filepath.Join(dir, id+".jsonl"), f.modified, f.modified); err != nil {
			t.Fatal(err)
		}
	}
}

// project makes a working folder, and the config folder to keep its
// sessions in; it returns both and the project folder.
func project(t *testing.T) (config, cwd, dir string) {
	t.Helper()
	config, cwd = t.TempDir(), filepath.Join(t.TempDir(), "my-project")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	stubWorktrees(t, nil)
	return config, cwd, Store{ConfigDir: config}.ProjectDir(cwd)
}

func TestListOneSession(t *testing.T) {
	config, cwd, dir := project(t)
	id := newUUID()
	fixture{prompt: "What is 2+2?", branch: "main", cwd: cwd}.write(t, dir, id)

	got, err := Store{ConfigDir: config}.List(cwd, 0, 0)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %+v, %v", got, err)
	}
	s := got[0]
	if s.ID != id || s.FirstPrompt != "What is 2+2?" || s.Summary != "What is 2+2?" || s.GitBranch != "main" ||
		s.Cwd != cwd || s.Size == 0 || s.LastModified.IsZero() || s.CustomTitle != "" {
		t.Errorf("List = %+v", s)
	}
}

func TestListSummary(t *testing.T) {
	for _, c := range []struct {
		name  string
		f     fixture
		want  string
		title string
	}{
		{"custom title first", fixture{prompt: "original question", summary: "auto summary", title: "My Custom Title"}, "My Custom Title", "My Custom Title"},
		{"summary before the prompt", fixture{prompt: "question", summary: "better summary"}, "better summary", ""},
		{"last prompt before the summary", fixture{prompt: "question", summary: "s", extra: []string{`{"type":"last-prompt","lastPrompt":"latest"}`}}, "latest", ""},
		{"generated title", fixture{prompt: "question", extra: []string{`{"type":"ai-title","aiTitle":"Generated"}`}}, "Generated", "Generated"},
		{"set title before a generated one", fixture{title: "Mine", extra: []string{`{"type":"ai-title","aiTitle":"Generated"}`}}, "Mine", "Mine"},
	} {
		config, cwd, dir := project(t)
		c.f.write(t, dir, newUUID())
		got, err := Store{ConfigDir: config}.List(cwd, 0, 0)
		if err != nil || len(got) != 1 || got[0].Summary != c.want || got[0].CustomTitle != c.title {
			t.Errorf("%s: List = %+v, %v", c.name, got, err)
		}
	}
}

func TestListNewestFirstInPages(t *testing.T) {
	config, cwd, dir := project(t)
	base := time.Now().Add(-time.Hour)
	ids := []string{newUUID(), newUUID(), newUUID(), newUUID()}
	for i, id := range ids { // ids[3] newest
		fixture{modified: base.Add(time.Duration(i) * time.Minute)}.write(t, dir, id)
	}
	for _, c := range []struct {
		limit, offset int
		want          []string
	}{
		{0, 0, []string{ids[3], ids[2], ids[1], ids[0]}},
		{2, 0, []string{ids[3], ids[2]}},
		{2, 2, []string{ids[1], ids[0]}},
		{0, 3, []string{ids[0]}},
		{5, 10, nil},
		{1, -1, []string{ids[3]}},
	} {
		got, err := Store{ConfigDir: config}.List(cwd, c.limit, c.offset)
		if err != nil || strings.Join(sessionIDs(got), ",") != strings.Join(c.want, ",") {
			t.Errorf("List(limit %d, offset %d) = %v, %v; want %v", c.limit, c.offset, sessionIDs(got), err, c.want)
		}
	}
}

func TestListSkips(t *testing.T) {
	config, cwd, dir := project(t)
	keep := newUUID()
	fixture{}.write(t, dir, keep)
	fixture{sidechain: true}.write(t, dir, newUUID())
	writeLines(t, dir, newUUID())                                          // empty
	writeLines(t, dir, newUUID(), `{"type":"permission-mode"}`)            // nothing to name it by
	fixture{}.write(t, dir, "not-a-uuid")                                  // not a session's name
	writeLines(t, filepath.Join(dir, keep, "subagents"), "agent-a1", "{}") // a subagent's folder
	if err := os.WriteFile(filepath.Join(dir, newUUID()+".txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Store{ConfigDir: config}.List(cwd, 0, 0)
	if err != nil || strings.Join(sessionIDs(got), ",") != keep {
		t.Errorf("List = %v, %v; want only %s", sessionIDs(got), err, keep)
	}
	if got, err := (Store{ConfigDir: config}).List(filepath.Join(cwd, "elsewhere"), 0, 0); err != nil || len(got) != 0 {
		t.Errorf("List of a folder with no sessions = %v, %v", got, err)
	}
	if got, err := (Store{ConfigDir: filepath.Join(config, "nothing")}).List("", 0, 0); err != nil || len(got) != 0 {
		t.Errorf("List with no config folder = %v, %v", got, err)
	}
}

func TestListEveryProject(t *testing.T) {
	config := t.TempDir()
	stubWorktrees(t, nil)
	a, b, shared := newUUID(), newUUID(), newUUID()
	old := time.Now().Add(-time.Hour)
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir("/one"), a)
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir("/two"), b)
	fixture{prompt: "old copy", modified: old}.write(t, Store{ConfigDir: config}.ProjectDir("/one"), shared)
	fixture{prompt: "new copy"}.write(t, Store{ConfigDir: config}.ProjectDir("/two"), shared)
	t.Setenv("CLAUDE_CONFIG_DIR", config)

	got, err := Store{}.List("", 0, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("List = %v, %v", sessionIDs(got), err)
	}
	for _, s := range got {
		if s.ID == shared && s.Summary != "new copy" {
			t.Errorf("a session in two folders is listed as %+v; want the newer", s)
		}
	}
}

func TestListWorktrees(t *testing.T) {
	config := t.TempDir()
	main, wt := canonical(t.TempDir()), canonical(t.TempDir())
	inside := filepath.Join(main, "packages", "app")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	stubWorktrees(t, map[string][]string{main: {main, wt}, inside: {main, wt}})
	inMain, inWt, inInside, elsewhere := newUUID(), newUUID(), newUUID(), newUUID()
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir(main), inMain)
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir(wt), inWt)
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir(inside), inInside)
	fixture{}.write(t, Store{ConfigDir: config}.ProjectDir(main+"-other"), elsewhere)

	for _, c := range []struct {
		cwd  string
		want []string
	}{
		{main, []string{inMain, inWt}},
		{inside, []string{inInside, inMain, inWt}},
	} {
		got, err := Store{ConfigDir: config}.List(c.cwd, 0, 0)
		if err != nil || !sameSet(sessionIDs(got), c.want) {
			t.Errorf("List(%s) = %v, %v; want %v", c.cwd, sessionIDs(got), err, c.want)
		}
	}
	for _, s := range mustList(t, config, main) {
		if s.ID == inWt && s.Cwd != wt {
			t.Errorf("a worktree's session has cwd %q, want %q", s.Cwd, wt)
		}
	}
}

func TestListDetailsFromTheEnds(t *testing.T) {
	config, cwd, dir := project(t)
	created := "2026-01-02T03:04:05.678Z"
	for _, c := range []struct {
		name  string
		lines []string
		check func(Info) bool
	}{
		{"cwd from the transcript, else the folder", []string{`{"type":"user","message":{"content":"hi"}}`}, func(s Info) bool { return s.Cwd == canonical(cwd) }},
		{"the branch recorded last", []string{`{"type":"user","gitBranch":"old","message":{"content":"hi"}}`, `{"type":"assistant","gitBranch":"new"}`}, func(s Info) bool { return s.GitBranch == "new" }},
		{"the last tag", []string{`{"type":"user","message":{"content":"hi"}}`, `{"type":"tag","tag":"a"}`, `{"type":"tag","tag":"b"}`}, func(s Info) bool { return s.Tag == "b" }},
		{"an empty tag clears it", []string{`{"type":"user","message":{"content":"hi"}}`, `{"type":"tag","tag":"a"}`, `{"type":"tag","tag":""}`}, func(s Info) bool { return s.Tag == "" }},
		{"a tool call's tag isn't one", []string{`{"type":"user","message":{"content":"hi"}}`, `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"tag":"v1.0"}}]}}`}, func(s Info) bool { return s.Tag == "" }},
		{"created at the first timestamp", []string{`{"type":"permission-mode"}`, `{"type":"user","timestamp":"` + created + `","message":{"content":"hi"}}`}, func(s Info) bool {
			return s.Created.Equal(time.Date(2026, 1, 2, 3, 4, 5, 678000000, time.UTC)) && !s.Created.After(s.LastModified)
		}},
		{"no time when it can't be read", []string{`{"type":"user","timestamp":"yesterday","message":{"content":"hi"}}`}, func(s Info) bool { return s.Created.IsZero() }},
	} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		writeLines(t, dir, newUUID(), c.lines...)
		if got := mustList(t, config, cwd); len(got) != 1 || !c.check(got[0]) {
			t.Errorf("%s: List = %+v", c.name, got)
		}
	}
}

func TestGet(t *testing.T) {
	config, cwd, dir := project(t)
	id, side := newUUID(), newUUID()
	fixture{prompt: "hello", extra: []string{`{"type":"tag","tag":"t"}`}}.write(t, dir, id)
	fixture{sidechain: true}.write(t, dir, side)

	for _, where := range []string{cwd, ""} {
		if s, err := (Store{ConfigDir: config}).Get(id, where); err != nil || s.ID != id || s.Summary != "hello" || s.Tag != "t" {
			t.Errorf("Get(cwd %q) = %+v, %v", where, s, err)
		}
	}
	if _, err := (Store{ConfigDir: config}).Get(id, t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get from another folder = %v", err)
	}
	for _, id := range []string{newUUID(), side} {
		if _, err := (Store{ConfigDir: config}).Get(id, cwd); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Get(%s) = %v", id, err)
		}
	}
	if _, err := (Store{ConfigDir: config}).Get("not-a-uuid", cwd); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get of a bad id = %v", err)
	}
}

// ReadInfo tells of the transcript it is given, whichever Get would find:
// the newer of a session's two here, which List tells of too. A named pipe
// isn't opened (it would wait for a writer), by List either.
func TestReadInfo(t *testing.T) {
	config, _, dir := project(t)
	moved := filepath.Join(config, "projects", "-zz-moved")
	id, side := newUUID(), newUUID()
	now := time.Now()
	fixture{prompt: "old", cwd: "/first", modified: now.Add(-time.Hour)}.write(t, dir, id)
	fixture{prompt: "new", cwd: "/zz/moved", modified: now}.write(t, moved, id)
	fixture{sidechain: true}.write(t, dir, side)

	for _, c := range []struct{ dir, summary, cwd string }{{dir, "old", "/first"}, {moved, "new", "/zz/moved"}} {
		s, err := ReadInfo(filepath.Join(c.dir, id+".jsonl"))
		if err != nil || s.ID != id || s.Summary != c.summary || s.Cwd != c.cwd {
			t.Errorf("ReadInfo in %s = %+v, %v", filepath.Base(c.dir), s, err)
		}
	}
	if got := mustList(t, config, ""); len(got) != 1 || got[0].Summary != "new" {
		t.Errorf("List = %+v", got)
	}
	pipe := filepath.Join(dir, newUUID()+".jsonl")
	mkfifo(t, pipe)
	if got := mustList(t, config, ""); len(got) != 1 {
		t.Errorf("List with a pipe = %+v", got)
	}
	for _, path := range []string{filepath.Join(dir, newUUID()+".jsonl"), filepath.Join(dir, side+".jsonl"), pipe} {
		if _, err := ReadInfo(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadInfo(%s) = %v", filepath.Base(path), err)
		}
	}
	for _, path := range []string{filepath.Join(dir, "notes.jsonl"), filepath.Join(dir, id), dir} {
		if _, err := ReadInfo(path); !errors.Is(err, ErrInvalidID) {
			t.Errorf("ReadInfo(%s) = %v", filepath.Base(path), err)
		}
	}
}

func mustList(t *testing.T, config, cwd string) []Info {
	t.Helper()
	got, err := Store{ConfigDir: config}.List(cwd, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func sessionIDs(infos []Info) []string {
	var out []string
	for _, s := range infos {
		out = append(out, s.ID)
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	in := map[string]int{}
	for _, s := range a {
		in[s]++
	}
	for _, s := range b {
		in[s]--
	}
	for _, n := range in {
		if n != 0 {
			return false
		}
	}
	return true
}

// A fork's details say where it was forked from, its first copied
// record's forkedFrom, whoever wrote it: Fork, or claude, whose fork may
// start with records that have none. A session that is no fork has none.
func TestInfoForkedFrom(t *testing.T) {
	config, cwd, dir := project(t)
	id, other, plain := newUUID(), newUUID(), newUUID()
	forkSource(t, dir, id)
	fork, err := Store{ConfigDir: config}.Fork(id, cwd, ForkOptions{})
	testNoErr(t, err)
	writeLines(t, dir, other,
		`{"type":"queue-operation","operation":"enqueue","sessionId":"`+other+`"}`,
		`{"type":"user","uuid":"n1","sessionId":"`+other+`","message":{"role":"user","content":"hi"},"forkedFrom":{"sessionId":"`+id+`","messageUuid":"u2"}}`,
		`{"type":"assistant","uuid":"n2","sessionId":"`+other+`","message":{"role":"assistant","content":"yes"},"forkedFrom":{"sessionId":"`+id+`","messageUuid":"a2"}}`)
	fixture{prompt: "no fork"}.write(t, dir, plain)
	for _, c := range []struct {
		id   string
		want *ForkedFrom
	}{{fork, &ForkedFrom{SessionID: id, MessageUUID: "u1"}}, {other, &ForkedFrom{SessionID: id, MessageUUID: "u2"}}, {plain, nil}, {id, nil}} {
		info, err := ReadInfo(filepath.Join(dir, c.id+".jsonl"))
		testNoErr(t, err)
		if (info.ForkedFrom == nil) != (c.want == nil) || c.want != nil && *info.ForkedFrom != *c.want {
			t.Errorf("%s: ForkedFrom %+v, want %+v", c.id, info.ForkedFrom, c.want)
		}
	}
	for _, info := range mustList(t, config, cwd) {
		if info.ID == fork && (info.ForkedFrom == nil || info.ForkedFrom.SessionID != id) {
			t.Errorf("List: the fork's ForkedFrom %+v", info.ForkedFrom)
		}
	}
}

// A fork whose first copied record is longer than the head List reads (a
// pasted screenshot, a long paste) still says where it came from: its
// forkedFrom comes at the record's end, past the head. So does a fork of
// that fork, and a session that is no fork still has none.
func TestInfoForkedFromPastTheHead(t *testing.T) {
	config, cwd, dir := project(t)
	id, plain := newUUID(), newUUID()
	long := strings.Repeat("x", 70<<10)
	writeLines(t, dir, id,
		`{"type":"user","uuid":"u1","sessionId":"`+id+`","message":{"role":"user","content":"`+long+`"},"timestamp":"2026-01-01T00:00:00.000Z"}`,
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"`+id+`","message":{"id":"msg_1","role":"assistant","content":"ok"},"timestamp":"2026-01-01T00:00:01.000Z"}`)
	writeLines(t, dir, plain,
		`{"type":"user","uuid":"p1","sessionId":"`+plain+`","message":{"role":"user","content":"`+long+`"},"timestamp":"2026-01-01T00:00:00.000Z"}`,
		`{"type":"custom-title","customTitle":"no fork","sessionId":"`+plain+`"}`)
	store := Store{ConfigDir: config}
	fork, err := store.Fork(id, cwd, ForkOptions{})
	testNoErr(t, err)
	again, err := store.Fork(fork, cwd, ForkOptions{})
	testNoErr(t, err)
	forkFirst := firstUUID(t, filepath.Join(dir, fork+".jsonl"))
	for _, c := range []struct {
		id   string
		want *ForkedFrom
	}{{fork, &ForkedFrom{SessionID: id, MessageUUID: "u1"}}, {again, &ForkedFrom{SessionID: fork, MessageUUID: forkFirst}}, {plain, nil}} {
		info, err := ReadInfo(filepath.Join(dir, c.id+".jsonl"))
		testNoErr(t, err)
		if (info.ForkedFrom == nil) != (c.want == nil) || c.want != nil && *info.ForkedFrom != *c.want {
			t.Errorf("%s: ForkedFrom %+v, want %+v", c.id, info.ForkedFrom, c.want)
		}
	}
	for _, info := range mustList(t, config, cwd) {
		if info.ID == fork && (info.ForkedFrom == nil || info.ForkedFrom.SessionID != id) {
			t.Errorf("List: the fork's ForkedFrom %+v", info.ForkedFrom)
		}
	}
}

// The rest of a cut record is read a chunk at a time: a forkedFrom key
// the chunks split is still found, the record's last one wins, and one in
// the next record isn't the cut record's.
func TestForkedFromAtChunks(t *testing.T) {
	dir := t.TempDir()
	want := ForkedFrom{SessionID: "s", MessageUUID: "m"}
	chunk := liteRead + len(forkKey)
	for _, c := range []struct {
		name string
		text func(pad int) string
		want *ForkedFrom
	}{
		{"split", func(pad int) string {
			return `{"x":"` + strings.Repeat("y", pad) + `","forkedFrom":{"sessionId":"s","messageUuid":"m"}}` + "\n"
		}, &want},
		{"last wins", func(pad int) string {
			return `{"x":{"forkedFrom":{"sessionId":"no","messageUuid":"no"}},"y":"` + strings.Repeat("y", pad) +
				`","forkedFrom":{"sessionId":"s","messageUuid":"m"}}` + "\n"
		}, &want},
		{"next record", func(pad int) string {
			return `{"x":"` + strings.Repeat("y", pad) + `"}` + "\n" + `{"forkedFrom":{"sessionId":"s","messageUuid":"m"}}` + "\n"
		}, nil},
	} {
		for pad := chunk - 2*len(forkKey); pad <= chunk+len(forkKey); pad++ {
			path := filepath.Join(dir, "t.jsonl")
			if err := os.WriteFile(path, []byte(c.text(pad)), 0o600); err != nil {
				t.Fatal(err)
			}
			got := forkedFromAt(path, 0)
			if (got == nil) != (c.want == nil) || c.want != nil && *got != *c.want {
				t.Fatalf("%s, pad %d: %+v, want %+v", c.name, pad, got, c.want)
			}
		}
	}
}

// firstUUID is the uuid of path's first record.
func firstUUID(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	testNoErr(t, err)
	first, _, _ := strings.Cut(string(b), "\n")
	var r struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal([]byte(first), &r); err != nil || r.UUID == "" {
		t.Fatalf("the first record's uuid: %q, %v", r.UUID, err)
	}
	return r.UUID
}
