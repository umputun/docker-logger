package logger

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
)

//go:generate moq -out mocks/log_client.go -pkg mocks -skip-ensure -fmt goimports . LogClient

// LogClient wraps DockerClient with the minimal interface
type LogClient interface {
	Logs(docker.LogsOptions) error
}

// LogStreamer connects and activates container's log stream with io.Writer
type LogStreamer struct {
	DockerClient  LogClient
	ContainerID   string
	ContainerName string

	LogWriter io.WriteCloser
	ErrWriter io.WriteCloser

	ctx    context.Context // nolint:containedctx
	cancel context.CancelFunc
	err    atomic.Value
}

// Go starts log streaming in a goroutine. It attaches to the container's log stream
// and writes to LogWriter/ErrWriter. Retries on Docker EOF errors with a 1s delay.
// After Wait() returns, use Err() to retrieve the error (if any) from the streaming goroutine.
func (l *LogStreamer) Go(ctx context.Context) *LogStreamer {
	log.Printf("[INFO] start log streamer for %s", l.ContainerName)
	l.ctx, l.cancel = context.WithCancel(ctx)

	go func() {
		logOpts := docker.LogsOptions{
			Container:         l.ContainerID,
			OutputStream:      l.LogWriter, // logs writer for stdout
			ErrorStream:       l.ErrWriter, // err writer for stderr
			Tail:              "10",
			Follow:            true,
			Stdout:            true,
			Stderr:            true,
			InactivityTimeout: time.Hour * 10000,
			Context:           l.ctx,
		}

		var err error
		for {
			err = l.DockerClient.Logs(logOpts) // this is blocking call. Will run until container up and will publish to streams
			// workaround https://github.com/moby/moby/issues/35370 with empty log, try read log as empty
			if err != nil && strings.HasPrefix(err.Error(), "error from daemon in stream: Error grabbing logs: EOF") {
				logOpts.Tail = ""
				time.Sleep(1 * time.Second) // prevent busy loop
				log.Print("[DEBUG] retry logger")
				continue
			}
			break
		}

		if err != nil && err != context.Canceled {
			l.err.Store(err)
			log.Printf("[WARN] stream from %s terminated with error %v", l.ContainerID, err)
			return
		}
		log.Printf("[INFO] stream from %s terminated", l.ContainerID)
	}()

	return l
}

// Err returns the error from the streaming goroutine, if any. Returns nil if the stream
// completed normally or was canceled. Should be called after Wait() returns.
func (l *LogStreamer) Err() error {
	v := l.err.Load()
	if v == nil {
		return nil
	}
	return v.(error)
}

// Close cancels the streaming context and waits for the cancellation to propagate.
// The stream goroutine will exit once the Docker client observes the cancellation.
func (l *LogStreamer) Close() {
	l.cancel()
	l.Wait()
	log.Printf("[DEBUG] close %s", l.ContainerID)
}

// Wait blocks until the streaming context is canceled, either by Close or parent context.
func (l *LogStreamer) Wait() {
	<-l.ctx.Done()
}
