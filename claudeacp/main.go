package claudeacp

import (
	"context"
	"log/slog"
	"os"
	ossignal "os/signal" // signal is a test helper's name
	"syscall"
)

// Main runs the agent on stdin and stdout, logging to stderr; it returns
// once the client has gone and every claude has exited. The claude it
// runs is $CLAUDE_CODE_EXECUTABLE, the variable claude-agent-acp reads
// too, else claude on PATH.
//
// Each claude stays in the agent's process group, as claude-agent-acp's
// does, so a client that kills the agent's group kills every claude with
// it, even one the agent had no time to close. SIGTERM and SIGINT end the
// agent as the end of stdin does: each session is closed, and its claude
// exits; a second one kills the agent at once.
//
// Main returns nil when the client went or a signal ended the agent, and
// otherwise what broke the connection (already logged), so that a
// command can exit with an error.
func Main() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := Config{CLIPath: os.Getenv("CLAUDE_CODE_EXECUTABLE"), Log: log, SharedGroup: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	ossignal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer ossignal.Stop(sigs)
	go func() {
		select {
		case sig := <-sigs:
			ossignal.Stop(sigs) // the next one has its default effect
			log.Info("closing the sessions on a signal", "signal", sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	err := Serve(ctx, os.Stdin, os.Stdout, cfg)
	if ctx.Err() != nil {
		return nil // a signal: the end the agent was asked for
	}
	if err != nil {
		log.Error("serving the client", "err", err)
	}
	return err
}
