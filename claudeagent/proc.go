package claudeagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/afrase/acp-go/process"
)

// Spec is how to start claude: the program, its arguments after its name,
// its environment and its folder, and whether it shares this process's
// group (Options.SharedGroup).
type Spec struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	SharedGroup bool
}

// Proc is a started claude: its pipes, and the Process to wait for and
// signal. Stdout and Stderr are read until they end, then closed (early,
// to give up on them); Stdin is closed by Close, or once claude exits.
type Proc struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Process
}

// Process is a started claude's process.
type Process interface {
	// Wait waits for it to exit, once: nil when it exits with 0, an
	// *ExitError when it exits with another code or is killed.
	Wait() error
	// Signal sends it a signal: to its process group, where there is one.
	// After it has exited Signal returns os.ErrProcessDone.
	Signal(sig os.Signal) error
}

// Spawner starts claude. ctx bounds the start only, not the process's
// life. Exec is the real one; claudetest.Fake runs a script instead.
type Spawner func(ctx context.Context, s Spec) (*Proc, error)

// ExitError is how claude ended when it didn't exit with 0.
type ExitError struct {
	Code   int       // its exit code; -1 when a signal killed it
	Signal os.Signal // the signal that killed it, or nil
	Stderr string    // the end of what it wrote on stderr
	// Result is the text of the last result claude reported as an error,
	// which says more than the exit code.
	Result string
}

func (e *ExitError) Error() string {
	switch {
	case e.Result != "" && e.Signal != nil:
		return fmt.Sprintf("claude returned an error result, then ended on a signal (%v): %s", e.Signal, e.Result)
	case e.Result != "":
		return fmt.Sprintf("claude returned an error result, then exited with code %d: %s", e.Code, e.Result)
	case e.Signal != nil:
		return fmt.Sprintf("claude ended on a signal (%v)%s", e.Signal, stderrSuffix(e.Stderr))
	default:
		return fmt.Sprintf("claude exited with code %d%s", e.Code, stderrSuffix(e.Stderr))
	}
}

func stderrSuffix(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

// Exec starts s as a process in a group of its own, so a signal reaches
// what claude started in that group too (its stdio MCP servers, its
// hooks). Claude Code runs each Bash command detached, leading a group of
// its own, so no signal to claude's group reaches those. Once claude has
// exited, Wait kills what it left in the group. On Windows the group is a
// console process group and a job object, which Wait, and os.Kill's
// Signal, end.
//
// With s.SharedGroup claude stays in this process's group instead: what
// kills that group kills claude and what stays in its group too, even
// when this process is killed first, but not claude's Bash commands.
// Signal then reaches claude alone, and what claude leaves running is
// left to that kill.
func Exec(ctx context.Context, s Spec) (*Proc, error) {
	// The process outlives the start, so ctx's end mustn't kill it.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), s.Path, s.Args...) //nolint:gosec // running the user's claude is the point
	cmd.Env, cmd.Dir = s.Env, s.Dir
	if !s.SharedGroup {
		process.OwnGroup(cmd)
	}
	pipes, err := process.Start(cmd, true)
	if err != nil {
		return nil, fmt.Errorf("claudeagent: start %s: %w", s.Path, err)
	}
	c := &child{cmd: cmd}
	if !s.SharedGroup {
		c.group = process.NewGroup(cmd)
	}
	return &Proc{Stdin: pipes.Stdin, Stdout: pipes.Stdout, Stderr: pipes.Stderr, Process: c}, nil
}

// child is a claude Exec started.
type child struct {
	cmd    *exec.Cmd
	group  *process.Group // nil when claude is in this process's group
	exited atomic.Bool
}

// Wait waits for claude, then kills what it left in its group.
func (c *child) Wait() error {
	err := c.cmd.Wait()
	c.exited.Store(true)
	if c.group != nil {
		_ = c.group.Sweep() // what it left running
		c.group.Release()
	}
	return exitError(err)
}

// Signal signals claude's group, or claude alone when the group is ours
// too.
func (c *child) Signal(sig os.Signal) error {
	if c.exited.Load() {
		return os.ErrProcessDone
	}
	if c.group == nil {
		return c.cmd.Process.Signal(sig)
	}
	return c.group.Signal(sig)
}

// exitError turns exec's error into an *ExitError.
func exitError(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	return &ExitError{Code: ee.ExitCode(), Signal: process.ExitSignal(err)}
}
