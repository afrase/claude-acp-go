package claudeacp

import (
	"cmp"
	"context"
	"log/slog"
	"os"
	ossignal "os/signal" // signal is a test helper's name
	"syscall"
)

// Main runs the agent on stdin and stdout, logging to stderr (cfg.Log, if
// set, takes its place); it returns once the client has gone and every
// claude has exited. The claude it runs is cfg.CLIPath, else
// $CLAUDE_CODE_EXECUTABLE (the variable claude-agent-acp reads too), else
// claude on PATH, and cfg.Spawn starts it (nil: claudeagent.Exec).
//
// Each claude stays in the agent's process group, as claude-agent-acp's
// does, so a client that kills the agent's group kills every claude with
// it, even one the agent had no time to close: Main sets cfg.SharedGroup.
// SIGTERM and SIGINT end the agent as the end of stdin does: each session
// is closed, and its claude exits; a second one has its default effect.
//
// Main returns nil when the client went or a signal ended the agent, and
// otherwise what broke the connection (already logged), so that a
// command can exit with an error.
func Main(cfg Config) error {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	log := cfg.Log
	cfg.CLIPath = cmp.Or(cfg.CLIPath, os.Getenv("CLAUDE_CODE_EXECUTABLE"))
	cfg.SharedGroup = true
	ctx, stop := ossignal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	signalled := make(chan struct{})
	defer context.AfterFunc(ctx, func() { // unregistered before stop runs, so a return logs nothing
		stop() // the next signal has its default effect
		log.Info("closing the sessions on a signal", "signal", context.Cause(ctx))
		close(signalled)
	})()
	err := Serve(ctx, os.Stdin, os.Stdout, cfg)
	if ctx.Err() != nil {
		<-signalled
		return nil // a signal: the end the agent was asked for
	}
	if err != nil {
		log.Error("serving the client", "err", err)
	}
	return err
}
