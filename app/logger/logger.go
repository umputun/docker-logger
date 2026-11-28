package logger

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
)

//go:generate moq -out mocks/log_client.go -pkg mocks -skip-ensure -fmt goimports . LogClient

// delays between reopens of a dropped stream: the first one, and the cap of the doubling
const (
	minReopenDelay = time.Second
	maxReopenDelay = 30 * time.Second
)

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
	done   chan struct{}
	err    atomic.Value
}

// Go starts log streaming in a goroutine. It attaches to the container's log stream
// and writes to LogWriter/ErrWriter. A stream that fails for any reason other than the daemon
// refusing it or a raw TTY stream, such as a connection dropped by a proxy, is reopened with a
// backoff from the second of the last line written.
// After Wait() returns, use Err() to retrieve the error (if any) from the streaming goroutine.
func (l *LogStreamer) Go(ctx context.Context) *LogStreamer {
	log.Printf("[INFO] start log streamer for %s", l.ContainerName)
	l.ctx, l.cancel = context.WithCancel(ctx)
	l.done = make(chan struct{})

	go func() {
		defer l.exit()
		if err := l.stream(); err != nil {
			l.err.Store(err)
			log.Printf("[WARN] stream from %s terminated with error %v", l.ContainerID, err)
			return
		}
		log.Printf("[INFO] stream from %s terminated", l.ContainerID)
	}()

	return l
}

// stream follows the container's log until the daemon ends it, which it does when the container
// stops, the context is canceled, the daemon refuses the request, or the stream is a raw TTY one.
// Any other error is retried.
func (l *LogStreamer) stream() error {
	// unix seconds of the last line written, or of the start if nothing is written yet
	var lastWrite atomic.Int64
	lastWrite.Store(time.Now().Unix())

	logOpts := docker.LogsOptions{
		Container:         l.ContainerID,
		OutputStream:      l.trackWrites(l.LogWriter, &lastWrite), // logs writer for stdout
		ErrorStream:       l.trackWrites(l.ErrWriter, &lastWrite), // err writer for stderr
		Tail:              "10",
		Follow:            true,
		Stdout:            true,
		Stderr:            true,
		InactivityTimeout: time.Hour * 10000,
		Context:           l.ctx,
	}

	delay := minReopenDelay
	for {
		started := time.Now()
		err := l.DockerClient.Logs(logOpts) // this is blocking call. Will run until container up and will publish to streams
		select {
		case <-l.ctx.Done():
			return nil // canceled by Close or the parent context
		default:
		}
		if err == nil || !l.reopenable(err) {
			return err // nil when the daemon ends the stream, which it does when the container stops
		}

		if time.Since(started) > maxReopenDelay {
			delay = minReopenDelay // the stream was up for a while, so this is a new failure rather than a repeated one
		}
		log.Printf("[WARN] stream from %s dropped with error %v, reopen in %v", l.ContainerID, err, delay)
		select {
		case <-l.ctx.Done():
			return nil
		case <-time.After(delay):
		}
		delay = min(delay*2, maxReopenDelay)

		// resume from the second of the last write, so that second's lines may repeat. The daemon filters
		// by each line's own timestamp, so lines stamped earlier but undelivered at the drop are skipped.
		// an empty Tail also works around https://github.com/moby/moby/issues/35370 with an empty log
		logOpts.Tail = ""
		logOpts.Since = lastWrite.Load()
	}
}

// reopenable tells whether a stream that failed with err is worth reopening. The daemon's own
// refusals, such as an unknown container or a log driver that can't be read, are final. So is
// a raw TTY stream, which the stdout/stderr demultiplexer rejects as "unrecognized stream" on
// every attempt. Anything else, like a dropped connection or a proxy's 502, may be gone on the
// next try.
func (l *LogStreamer) reopenable(err error) bool {
	if _, ok := errors.AsType[*docker.NoSuchContainer](err); ok {
		return false
	}
	if apiErr, ok := errors.AsType[*docker.Error](err); ok {
		return apiErr.Status >= http.StatusInternalServerError && apiErr.Status != http.StatusNotImplemented
	}
	return !strings.HasPrefix(err.Error(), "unrecognized stream:")
}

// trackWrites wraps w to store the time of each successful write in last. A nil w stays nil.
func (l *LogStreamer) trackWrites(w io.Writer, last *atomic.Int64) io.Writer {
	if w == nil {
		return nil
	}
	return &trackedWriter{w: w, last: last}
}

type trackedWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (t *trackedWriter) Write(p []byte) (int, error) {
	now := time.Now().Unix()
	n, err := t.w.Write(p)
	if err == nil {
		t.last.Store(now)
	}
	return n, err
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

// Close cancels the streaming context and waits for the stream goroutine to exit,
// so nothing writes to LogWriter/ErrWriter after it returns.
func (l *LogStreamer) Close() {
	l.cancel()
	l.Wait()
	log.Printf("[DEBUG] close %s", l.ContainerID)
}

// Wait blocks until the stream goroutine exits: the stream ended, failed for good,
// or was canceled by Close or the parent context.
func (l *LogStreamer) Wait() {
	<-l.done
}

// exit marks the stream goroutine as exited and then releases its context, so whoever sees the
// context canceled by a stream that ended by itself also sees Exited report true
func (l *LogStreamer) exit() {
	close(l.done)
	l.cancel()
}

// Exited tells, without blocking, whether the stream goroutine has exited, as Wait would see it.
// A stream waiting to be reopened has not exited.
func (l *LogStreamer) Exited() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}
