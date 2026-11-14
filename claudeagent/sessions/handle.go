package sessions

import "context"

// Transcript is one session's transcript, found once by Store.Open. Its
// methods read the file as it is when called, without looking for it
// again: a caller reading several things of a session (its messages, its
// subagents and theirs, its queued prompts) finds it, and runs git, once.
type Transcript struct {
	id, path string
}

// Open finds session id's transcript, looking from cwd as Store's methods
// do. ctx ends the search, git's listing of cwd's worktrees among it. An
// error matching fs.ErrNotExist says the session isn't there.
func (s Store) Open(ctx context.Context, id, cwd string) (*Transcript, error) {
	path, err := s.find(id, cwd, func(dir string) []string { return worktrees(ctx, dir) })
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr // git's list was cut short: what was found may not be the session's first
	}
	if err != nil {
		return nil, err
	}
	return &Transcript{id: id, path: path}, nil
}

// open is Open for the Store methods that take no context.
func (s Store) open(id, cwd string) (*Transcript, error) {
	path, err := s.find(id, cwd, anyWorktrees)
	if err != nil {
		return nil, err
	}
	return &Transcript{id: id, path: path}, nil
}

// ID is the session's id.
func (t *Transcript) ID() string { return t.id }

// Path is the transcript's file.
func (t *Transcript) Path() string { return t.path }
