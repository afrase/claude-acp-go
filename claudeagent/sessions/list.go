// Ported from the Claude Agent SDK for Python (commit 27858cbb,
// src/claude_agent_sdk/_internal/sessions.py), MIT License,
// Copyright (c) 2025 Anthropic, PBC: see ../NOTICE. Modified: restructured
// as Go, the functions a Store's methods.

package sessions

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// liteRead is how much of a transcript's start and end List reads: its
// details sit in the first records and the last ones.
const liteRead = 64 << 10

// Info is what List, Get and ReadInfo tell of a session, read from the first
// and last 64 KiB of its transcript and from the file.
type Info struct {
	// ID is the session's id, the transcript's name.
	ID string
	// Summary names the session: its custom title, else the last prompt
	// or summary the CLI recorded, else its first prompt.
	Summary string
	// CustomTitle is the title set for the session (by the user or a fork),
	// else the one the CLI generated; "" when there is none.
	CustomTitle string
	// FirstPrompt is the first prompt the user typed, on one line and at
	// most 200 characters; a slash command's name when there is no other.
	FirstPrompt string
	// GitBranch is the branch recorded last.
	GitBranch string
	// Cwd is the session's working directory as recorded, else the folder
	// it was listed for (ReadInfo has none: "").
	Cwd string
	// Tag is the session's last tag; "" when it has none.
	Tag string
	// LastModified is when the transcript was last written.
	LastModified time.Time
	// Created is the time of its first record; zero when none has one.
	Created time.Time
	// Size is the transcript's size in bytes.
	Size int64
	// ForkedFrom is, for a fork (made by claude --fork-session or by
	// Fork), where its first copied record came from: the session it was
	// forked from, and that record's uuid there (the first message the
	// fork copied, not the one it was forked at). nil for a session that
	// is no fork. Read from the first 64 KiB, and of a record they cut
	// short (a fork's first, holding a pasted screenshot, say) from the
	// rest of it: nil, too, when none of those says, as for a first copied
	// record over 64 MiB.
	ForkedFrom *ForkedFrom
}

// List returns the sessions of cwd and of its git repository's other
// worktrees, newest first; with no cwd, every project's. It skips
// subagents' transcripts and ones with nothing to name them by. offset
// sessions are skipped and at most limit returned (0 = all). Folders that
// can't be read are skipped.
func (s Store) List(cwd string, limit, offset int) ([]Info, error) {
	projects, err := s.projects()
	if err != nil {
		return nil, err
	}
	var infos []Info
	if cwd == "" {
		for _, dir := range subdirs(projects) {
			infos = append(infos, readProject(filepath.Join(projects, dir), "")...)
		}
		infos = newestByID(infos)
	} else {
		infos = listProject(projects, canonical(cwd))
	}
	slices.SortStableFunc(infos, func(a, b Info) int { return b.LastModified.Compare(a.LastModified) })
	infos = infos[min(max(offset, 0), len(infos)):]
	if limit > 0 && limit < len(infos) {
		infos = infos[:limit]
	}
	return infos, nil
}

// listProject lists the sessions of dir and of its repository's worktrees.
func listProject(projects, dir string) []Info {
	wts := anyWorktrees(dir)
	own, ok := findProjectDir(projects, dir)
	if len(wts) <= 1 {
		if !ok {
			return nil
		}
		return readProject(own, dir)
	}
	// dir itself first: a folder inside a worktree isn't one of the
	// worktrees' names.
	var infos []Info
	seen := map[string]bool{}
	if ok {
		seen[filepath.Base(own)] = true
		infos = readProject(own, dir)
	}
	// The longest names first, so a worktree inside another gets its own.
	type tree struct{ path, name string }
	trees := make([]tree, len(wts))
	for i, wt := range wts {
		trees[i] = tree{wt, folderName(wt)}
	}
	slices.SortStableFunc(trees, func(a, b tree) int { return len(b.name) - len(a.name) })
	for _, folder := range subdirs(projects) {
		if seen[folder] {
			continue
		}
		for _, wt := range trees {
			if folder == wt.name || longMatch(projects, folder, wt.path, wt.name) {
				seen[folder] = true
				infos = append(infos, readProject(filepath.Join(projects, folder), wt.path)...)
				break
			}
		}
	}
	return newestByID(infos)
}

// newestByID keeps the newest of each session listed in more than one
// folder.
func newestByID(infos []Info) []Info {
	at := map[string]int{}
	var out []Info
	for _, info := range infos {
		i, ok := at[info.ID]
		switch {
		case !ok:
			at[info.ID] = len(out)
			out = append(out, info)
		case info.LastModified.After(out[i].LastModified):
			out[i] = info
		}
	}
	return out
}

// readProject reads the details of each session in a project folder; cwd
// is the folder's working directory, when known.
func readProject(dir, cwd string) []Info {
	entries, _ := os.ReadDir(dir)
	var infos []Info
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || checkID(id) != nil {
			continue
		}
		l, ok := readLite(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if info, ok := l.info(id, cwd); ok {
			infos = append(infos, info)
		}
	}
	return infos
}

// Get returns the details of session id; an error matching fs.ErrNotExist
// when it isn't there, is a subagent's, or has nothing to name it by.
func (s Store) Get(id, cwd string) (*Info, error) {
	path, err := s.find(id, cwd, anyWorktrees)
	if err != nil {
		return nil, err
	}
	if cwd != "" {
		cwd = canonical(cwd)
	}
	return readInfo(path, id, cwd)
}

// ReadInfo returns the details of the session whose transcript is the file
// at path, as Get tells them: for a caller that walks the project folders
// itself, and so knows which of a session's transcripts it means when
// more than one folder has one (Get takes the first it finds). Cwd is ""
// when no record says it. ErrInvalidID when the file isn't named
// <id>.jsonl; an error matching fs.ErrNotExist, as Get's, when it isn't
// there or can't be read, is a subagent's, or has nothing to name it by.
func ReadInfo(path string) (*Info, error) {
	id, ok := strings.CutSuffix(filepath.Base(path), ".jsonl")
	if !ok {
		return nil, fmt.Errorf("%q: %w", path, ErrInvalidID)
	}
	if err := checkID(id); err != nil {
		return nil, err
	}
	return readInfo(path, id, "")
}

// readInfo reads session id's details from its transcript at path; cwd is
// its working directory, when known.
func readInfo(path, id, cwd string) (*Info, error) {
	l, ok := readLite(path)
	if !ok {
		return nil, notFound("session " + id)
	}
	info, ok := l.info(id, cwd)
	if !ok {
		return nil, notFound("session " + id + "'s details")
	}
	return &info, nil
}

// lite is a transcript's start and end, its size and when it was written.
type lite struct {
	head, tail string
	size       int64
	modified   time.Time
	// path is the transcript's, and cut the offset of the first record the
	// head doesn't hold whole (one it cut short, or the next past its end);
	// -1 when the file ends in the head.
	path string
	cut  int64
}

// readLite reads path's first and last 64 KiB; false for an empty file or
// one that can't be read. What isn't a regular file is never opened:
// opening a named pipe would wait for its writer.
func readLite(path string) (lite, bool) {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return lite{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return lite{}, false
	}
	defer f.Close() //nolint:errcheck // read-only
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return lite{}, false
	}
	buf := make([]byte, liteRead)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return lite{}, false
	}
	l := lite{head: strings.ToValidUTF8(string(buf[:n]), "�"), size: fi.Size(), modified: fi.ModTime(), path: path, cut: -1}
	if fi.Size() > int64(n) {
		l.cut = int64(bytes.LastIndexByte(buf[:n], '\n') + 1)
	}
	l.tail = l.head
	if fi.Size() > liteRead {
		n, _ = f.ReadAt(buf, fi.Size()-liteRead)
		l.tail = strings.ToValidUTF8(string(buf[:n]), "�")
	}
	return l, true
}

// customTitle is the session's title: the user's, else the one claude
// made, the last of each, from the end first.
func (l lite) customTitle() string {
	return cmp.Or(lastString(l.tail, "customTitle"), lastString(l.head, "customTitle"),
		lastString(l.tail, "aiTitle"), lastString(l.head, "aiTitle"))
}

// info reads a session's details from its transcript's start and end;
// false for a subagent's transcript or one with nothing to name it by.
func (l lite) info(id, cwd string) (Info, bool) {
	first, _, _ := strings.Cut(l.head, "\n")
	if strings.Contains(first, `"isSidechain":true`) || strings.Contains(first, `"isSidechain": true`) {
		return Info{}, false
	}
	info := Info{ID: id, Size: l.size, LastModified: l.modified}
	info.CustomTitle = l.customTitle()
	info.FirstPrompt = firstPrompt(l.head)
	info.Summary = cmp.Or(info.CustomTitle, lastString(l.tail, "lastPrompt"), lastString(l.tail, "summary"), info.FirstPrompt)
	if info.Summary == "" {
		return Info{}, false
	}
	info.GitBranch = cmp.Or(lastString(l.tail, "gitBranch"), firstString(l.head, "gitBranch"))
	info.Cwd = cmp.Or(firstString(l.head, "cwd"), cwd)
	// Only a tag record's tag: a tool call's input may say "tag" too.
	for _, line := range slices.Backward(strings.Split(l.tail, "\n")) {
		if strings.HasPrefix(line, `{"type":"tag"`) {
			info.Tag = lastString(line, "tag")
			break
		}
	}
	if ts := firstString(l.head, "timestamp"); ts != "" {
		info.Created = parseTime(ts)
	}
	info.ForkedFrom = l.forkedFrom()
	return info, true
}

// forkedFrom is the first forkedFrom a record of the transcript's start
// has; nil when none has one. Both claude and Fork write it at a record's
// end, so the records the head doesn't hold whole (a pasted screenshot, a
// long paste, a long queued prompt) are read past it to their ends: the
// one it cut short always, as before, and the next ones while fewer than
// forkRecords have been seen from the file's start. A transcript whose
// head holds forkRecords whole records costs no more than that cut
// record. A long session that is no fork costs up to forkRecords long
// records, at most forkScanLimit bytes past the head, read a chunk at a
// time, where it cost one.
func (l lite) forkedFrom() *ForkedFrom {
	for line := range strings.SplitSeq(l.head, "\n") {
		if !strings.Contains(line, `"forkedFrom"`) {
			continue
		}
		var r struct {
			ForkedFrom *ForkedFrom `json:"forkedFrom"`
		}
		if json.Unmarshal([]byte(line), &r) == nil && r.ForkedFrom != nil && r.ForkedFrom.SessionID != "" {
			return r.ForkedFrom
		}
	}
	if l.cut < 0 {
		return nil
	}
	records := forkRecords - strings.Count(l.head, "\n")
	if !strings.HasSuffix(l.head, "\n") { // the head cut a record short: read it all the same
		records = max(records, 1)
	}
	if records <= 0 {
		return nil
	}
	return forkedFromAt(l.path, l.cut, records)
}

// forkRecords is how many of a transcript's first records forkedFrom
// looks in. A fork says so in its first records: Fork and claude write
// forkedFrom on each record they copy, and before the first of those
// claude writes only the queue-operation records of prompts queued as the
// fork began. So a small number is enough, and it keeps what a session
// that is no fork costs small.
const forkRecords = 8

// forkScanLimit is how far past where it starts forkedFromAt reads.
const forkScanLimit = 64 << 20

// forkKey is a forkedFrom key as claude and Fork write it. Unescaped
// quotes can't be in a JSON string, so a match is a key.
const forkKey = `"forkedFrom":`

// forkedFromAt reads path's records from offset at, a chunk at a time, to
// the first of them, at most records, whose last forkedFrom key decodes,
// and returns that value; nil when none has one within forkScanLimit.
func forkedFromAt(path string, at int64, records int) *ForkedFrom {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close() //nolint:errcheck // read-only
	r := bufio.NewReaderSize(io.NewSectionReader(f, at, forkScanLimit), liteRead)
	window := make([]byte, 0, liteRead+len(forkKey))
	var tail [len(forkKey)]byte // the record's last bytes so far, which a key may span
	keep, found := 0, int64(-1)
	for off := int64(0); records > 0; {
		chunk, err := r.ReadSlice('\n')
		window = append(append(window[:0], tail[:keep]...), chunk...)
		if i := bytes.LastIndex(window, []byte(forkKey)); i >= 0 {
			found = off - int64(keep) + int64(i)
		}
		off += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			keep = copy(tail[:], window[len(window)-min(len(forkKey)-1, len(window)):])
			continue
		}
		if found >= 0 {
			var ff ForkedFrom
			d := json.NewDecoder(io.NewSectionReader(f, at+found+int64(len(forkKey)), 4<<10))
			if d.Decode(&ff) == nil && ff.SessionID != "" {
				return &ff
			}
		}
		if err != nil {
			return nil
		}
		records, keep, found = records-1, 0, -1
	}
	return nil
}

// parseTime reads a time as the CLI writes them (JavaScript's
// toISOString); zero when it can't.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
