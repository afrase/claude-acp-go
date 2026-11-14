package sessions

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A Transcript is found, and git run, once: what is read of it then,
// its subagents' messages among it, doesn't look for it again.
func TestOpenFindsOnce(t *testing.T) {
	config, cwd, dir := project(t)
	var gits atomic.Int32
	worktrees = func(context.Context, string) []string { gits.Add(1); return nil } // project's Cleanup restores it
	id := newUUID()
	transcript(t, dir, id, text("user", "u", "", "go"),
		rec("attachment", "q", "u", "attachment", record{"type": "queued_command", "prompt": "later"}),
		text("assistant", "a", "q", "ok"))
	agents := filepath.Join(dir, id, "subagents")
	transcript(t, agents, "agent-a1", text("user", "a-u", "", "task"),
		rec("attachment", "a-q", "a-u", "attachment", record{"type": "queued_command", "prompt": "note"}),
		text("assistant", "a-a", "a-q", "done"))
	transcript(t, agents, "agent-b2", text("user", "b-u", "", "task"))

	tr, err := Store{ConfigDir: config}.Open(context.Background(), id, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if tr.ID() != id || filepath.Dir(tr.Path()) != dir {
		t.Errorf("Open = %s %s", tr.ID(), tr.Path())
	}
	ms, err := tr.Messages()
	if err != nil || messageUUIDs(ms) != "u a" {
		t.Errorf("Messages = %v %v", messageUUIDs(ms), err)
	}
	if q, err := tr.QueuedPrompts(); err != nil || len(q) != 1 || q[0].Prompt != "later" {
		t.Errorf("QueuedPrompts = %+v %v", q, err)
	}
	if last, err := tr.LastRecord("a"); err != nil || last != "a" {
		t.Errorf("LastRecord = %q %v", last, err)
	}
	agentsFound := tr.Subagents()
	if len(agentsFound) != 2 {
		t.Fatalf("Subagents = %+v", agentsFound)
	}
	for _, a := range agentsFound {
		ms, err := a.Messages()
		if err != nil || len(ms) == 0 {
			t.Errorf("%s's Messages = %+v %v", a.ID, ms, err)
		}
		if _, err := a.QueuedPrompts(); err != nil {
			t.Errorf("%s's QueuedPrompts: %v", a.ID, err)
		}
	}
	if q, _ := agentsFound[0].QueuedPrompts(); len(q) != 1 || q[0].Prompt != "note" {
		t.Errorf("a1's QueuedPrompts = %+v", q)
	}
	if n := gits.Load(); n != 1 {
		t.Errorf("git ran %d times", n)
	}
	if _, err := (Subagent{ID: "x"}).Messages(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a Subagent not found by Subagents: %v", err)
	}
}

// Open's ctx reaches git, and a search it ended finds nothing.
func TestOpenEndsWithContext(t *testing.T) {
	config, cwd, dir := project(t)
	var sawEnded atomic.Bool
	worktrees = func(ctx context.Context, _ string) []string { sawEnded.Store(ctx.Err() != nil); return nil }
	id := newUUID()
	transcript(t, dir, id, text("user", "u", "", "go"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Store{ConfigDir: config}).Open(ctx, id, cwd); !errors.Is(err, context.Canceled) {
		t.Errorf("Open with an ended ctx = %v", err)
	}
	if !sawEnded.Load() {
		t.Error("git didn't get Open's ctx")
	}
}
