// Ported from the Claude Agent SDK for Python (commit 27858cbb,
// src/claude_agent_sdk/_internal/sessions.py), MIT License,
// Copyright (c) 2025 Anthropic, PBC: see ../NOTICE. Modified: restructured
// as Go, the functions a Store's methods; folder names are counted and
// hashed by UTF-16 unit, as the CLI does.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"uuid"
)

// maxName is the longest folder name the CLI keeps whole; a longer one is
// cut there and given a hash of the path.
const maxName = 200

// ErrInvalidID is the error for an id that isn't a session's: the CLI
// names sessions by UUID, so no session has it.
var ErrInvalidID = errors.New("sessions: not a session id")

var idRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// checkID refuses anything but a UUID, which is what the CLI names
// sessions; it also keeps a path out of a file name.
func checkID(id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("%q: %w", id, ErrInvalidID)
	}
	return nil
}

// newUUID returns a random (version 4) UUID, as the CLI names sessions and
// records.
func newUUID() string { return uuid.NewV4().String() }

// notFound is the error for a session or subagent that isn't on disk; it
// matches fs.ErrNotExist.
func notFound(what string) error {
	return fmt.Errorf("sessions: %s: %w", what, fs.ErrNotExist)
}

// Store is the CLI's session transcripts in one config folder. The zero
// Store is the CLI's default folder.
//
// Its methods that read or change one session take the session's id, then
// the working directory to look from: its project folder first, then its
// git repository's other worktrees' (the CLI resumes a session from any of
// them); "" looks in every project folder. Each looks for the session
// afresh, running git to list the worktrees; to read several things of
// one session, Open it once and read them from the Transcript.
type Store struct {
	// ConfigDir is the CLI's config folder; "" is $CLAUDE_CONFIG_DIR, else
	// ~/.claude.
	ConfigDir string
}

// projects returns the projects folder in the config folder.
func (s Store) projects() (string, error) {
	configDir := s.ConfigDir
	if configDir == "" {
		configDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("sessions: finding Claude's config folder: %w", err)
		}
		configDir = filepath.Join(home, ".claude")
	}
	return filepath.Join(configDir, "projects"), nil
}

// ProjectDir returns the folder in which the CLI keeps the transcripts of
// sessions run in cwd: cwd's real path with every UTF-16 unit that isn't an
// ASCII letter or digit replaced by "-", and past 200 characters cut and
// followed by a base-36 hash of the path. It returns "" when ConfigDir is ""
// and there is no home folder.
//
// The CLI also puts the path in Unicode NFC first; ProjectDir doesn't (the
// standard library has no NFC), so a path spelled with decomposed accents
// gets another name. The CLI's own builds may hash a long path otherwise
// (the Python SDK says Bun's hash); reads here then find the folder by its
// first 200 characters and the working directory its transcripts record.
func (s Store) ProjectDir(cwd string) string {
	projects, err := s.projects()
	if err != nil {
		return ""
	}
	return filepath.Join(projects, folderName(canonical(cwd)))
}

// canonical is path as the CLI names it: absolute, links resolved.
func canonical(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// folderName is the name of path's project folder (see ProjectDir).
func folderName(path string) string {
	units := utf16.Encode([]rune(path))
	b := make([]byte, len(units))
	var hash int32 // JavaScript's string hash: h*31 + unit, kept to 32 bits
	for i, u := range units {
		hash = hash*31 + int32(u)
		if 'a' <= u && u <= 'z' || 'A' <= u && u <= 'Z' || '0' <= u && u <= '9' {
			b[i] = byte(u)
		} else {
			b[i] = '-'
		}
	}
	if len(b) <= maxName {
		return string(b)
	}
	h := int64(hash)
	if h < 0 {
		h = -h
	}
	return string(b[:maxName]) + "-" + strconv.FormatInt(h, 36)
}

// findProjectDir returns path's project folder in projects: the one
// folderName names, else, for a long path, one under another hash (see
// longMatch).
func findProjectDir(projects, path string) (string, bool) {
	name := folderName(path)
	if fi, err := os.Stat(filepath.Join(projects, name)); err == nil && fi.IsDir() {
		return filepath.Join(projects, name), true
	}
	if len(name) <= maxName {
		return "", false
	}
	for _, dir := range subdirs(projects) {
		if longMatch(projects, dir, path, name) {
			return filepath.Join(projects, dir), true
		}
	}
	return "", false
}

// longMatch says folder, in projects, is the long path's (named name by
// folderName) under the hash of another CLI build: cut at the same 200
// characters, and its transcripts recorded in path. The cut alone isn't
// enough: two deep paths can share their first 200 characters.
func longMatch(projects, folder, path, name string) bool {
	return len(name) > maxName && strings.HasPrefix(folder, name[:maxName]+"-") &&
		recordedCwd(filepath.Join(projects, folder)) == path
}

// recordedCwd is the working directory the transcripts in dir record, the
// first that says one; "" when none does.
func recordedCwd(dir string) string {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") || !e.Type().IsRegular() {
			continue
		}
		if l, ok := readLite(filepath.Join(dir, e.Name())); ok {
			if cwd := firstString(l.head, "cwd"); cwd != "" {
				return cwd
			}
		}
	}
	return ""
}

// subdirs lists the folders in dir by name; none when it can't be read.
func subdirs(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// lister lists the worktrees of the git repository a folder is in.
type lister func(dir string) []string

// anyWorktrees is the lister of the Store methods that take no context:
// git runs to its end, 5 s at most. A caller with a context uses Open.
var anyWorktrees lister = func(dir string) []string { return worktrees(context.Background(), dir) }

// worktrees lists the worktrees of the git repository dir is in, the main
// one first; none outside a repository or without git. A session started in
// one worktree is found from another, as the CLI's own resume does. It is a
// variable so tests can stand in for git. ctx ends the git it runs, which
// has 5 s at most.
var worktrees = func(ctx context.Context, dir string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Listing runs no hook or filter; fsmonitor is off so the repository's
	// config can't start a program either.
	cmd := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseWorktrees(string(out))
}

// parseWorktrees reads the paths out of `git worktree list --porcelain`.
func parseWorktrees(out string) []string {
	var paths []string
	for line := range strings.SplitSeq(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

// candidates returns the project folders where a session of cwd may be, in
// the order to look: cwd's own, then its repository's other worktrees'; with
// no cwd, every project folder. list lists the worktrees.
func candidates(projects, cwd string, list lister) []string {
	if cwd == "" {
		dirs := subdirs(projects)
		for i, d := range dirs {
			dirs[i] = filepath.Join(projects, d)
		}
		return dirs
	}
	dir := canonical(cwd)
	var out []string
	if pd, ok := findProjectDir(projects, dir); ok {
		out = append(out, pd)
	}
	for _, wt := range list(dir) {
		if wt == dir {
			continue
		}
		if pd, ok := findProjectDir(projects, wt); ok {
			out = append(out, pd)
		}
	}
	return out
}

// find returns the path of session id's transcript, looking where
// candidates says. Only a file with something in it counts, as the CLI
// writes an empty one in some folders.
func (s Store) find(id, cwd string, list lister) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	projects, err := s.projects()
	if err != nil {
		return "", err
	}
	for _, dir := range candidates(projects, cwd, list) {
		path := filepath.Join(dir, id+".jsonl")
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Size() > 0 {
			return path, nil
		}
	}
	return "", notFound("session " + id)
}
