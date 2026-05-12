package workflow

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/svrc/cyberdeck/pkg/hypervisor"
)

// NewClient connects to a Temporal frontend (e.g. "localhost:7233" for
// `temporal server start-dev`). Single line wrapper; exists so future
// auth/TLS options have one place to land.
func NewClient(addr string) (client.Client, error) {
	return client.Dial(client.Options{HostPort: addr})
}

// NewWorker builds a worker.Worker that registers the cyberdeck workflows
// and activities bound to the given Hypervisor. Caller is responsible for
// starting it (worker.Start / worker.Run) and stopping it (worker.Stop).
func NewWorker(c client.Client, h hypervisor.Hypervisor) worker.Worker {
	w := worker.New(c, TaskQueue, worker.Options{})
	w.RegisterWorkflow(CreateNestedESXi)
	w.RegisterActivity(&Activities{H: h})
	return w
}

// RunWorker is the long-running daemon flavour: starts a worker, blocks
// until SIGINT/SIGTERM (or the provided ctx is cancelled), then stops
// cleanly. Used by `cyberdeck server`.
func RunWorker(ctx context.Context, c client.Client, h hypervisor.Hypervisor) error {
	w := NewWorker(c, h)

	if err := w.Start(); err != nil {
		return fmt.Errorf("worker start: %w", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case <-ctx.Done():
	case sig := <-stop:
		fmt.Fprintf(os.Stderr, "received %s, stopping worker...\n", sig)
	}

	w.Stop()
	return nil
}
