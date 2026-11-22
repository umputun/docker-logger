package logger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/docker-logger/app/logger/mocks"
)

func TestLogStreamer_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)
	require.Eventually(t, func() bool { return len(mock.LogsCalls()) >= 1 },
		5*time.Second, 10*time.Millisecond, "should have called Logs")
	cancel()
	l.Wait()
	assert.Len(t, mock.LogsCalls(), 1)
}

func TestLogStreamer_Close(t *testing.T) {
	ctx := context.Background()
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)
	require.Eventually(t, func() bool { return len(mock.LogsCalls()) >= 1 },
		5*time.Second, 10*time.Millisecond, "should have called Logs")
	l.Close()
	assert.Len(t, mock.LogsCalls(), 1)
}

func TestLogStreamer_CloseWaitsForInFlightWrite(t *testing.T) {
	w := &eventWriter{}
	started := make(chan struct{})
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		close(started)
		<-opts.Context.Done()
		time.Sleep(50 * time.Millisecond) // the client is still copying out the container's last line when the cancel comes
		_, _ = opts.OutputStream.Write([]byte("last line\n"))
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock, LogWriter: w, ErrWriter: w}
	l = l.Go(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Logs was not called")
	}

	// what closeStreamer does on a container stop: close the streamer, then its writers
	l.Close()
	require.NoError(t, w.Close())
	assert.Equal(t, []string{"write last line\n", "close"}, w.events(), "the last line should be written before the writer is closed")
}

func TestLogStreamer_NormalCompletion(t *testing.T) {
	ctx := context.Background()
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		return nil // immediate success
	}}

	buf := &bytes.Buffer{}
	l := &LogStreamer{
		ContainerID:   "test_id",
		ContainerName: "test_name",
		DockerClient:  mock,
		LogWriter:     nopWriteCloser{buf},
		ErrWriter:     nopWriteCloser{buf},
	}
	l = l.Go(ctx)
	l.Wait() // the stream goroutine exits on its own when the daemon ends the stream
	assert.Len(t, mock.LogsCalls(), 1)
	require.NoError(t, l.Err())
	l.Close()
}

func TestLogStreamer_RetryOnDockerEOF(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var callCount atomic.Int32
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		n := callCount.Add(1)
		if n == 1 {
			return errors.New("error from daemon in stream: Error grabbing logs: EOF")
		}
		// second call should have empty Tail
		assert.Empty(t, opts.Tail, "tail should be empty on retry")
		assert.Positive(t, opts.Since, "retry should continue from the start of the stream")
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)

	// wait for retry to happen using polling instead of fixed sleep
	require.Eventually(t, func() bool { return int(callCount.Load()) >= 2 },
		5*time.Second, 50*time.Millisecond, "should have retried at least once")
	cancel()
	l.Wait()

	assert.GreaterOrEqual(t, len(mock.LogsCalls()), 2)
}

func TestLogStreamer_ErrorTermination(t *testing.T) {
	ctx := context.Background()
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		return &docker.Error{Status: http.StatusNotFound, Message: "No such container: test_id"}
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)

	l.Wait() // a refusal from the daemon is final, so the goroutine exits on its own
	assert.Len(t, mock.LogsCalls(), 1)
	l.Close()
}

func TestLogStreamer_ReopenAfterDroppedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	start := time.Now().Unix()
	reopened := make(chan docker.LogsOptions, 1)
	var callCount atomic.Int32
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		if callCount.Add(1) == 1 {
			_, _ = opts.OutputStream.Write([]byte("before drop\n"))
			return io.ErrUnexpectedEOF // what a proxy closing the connection mid-stream looks like
		}
		reopened <- opts
		_, _ = opts.OutputStream.Write([]byte("after drop\n"))
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	logBuf := &bytes.Buffer{} // read only after Wait, when the stream goroutine is gone
	l := &LogStreamer{
		ContainerID:   "test_id",
		ContainerName: "test_name",
		DockerClient:  mock,
		LogWriter:     nopWriteCloser{logBuf},
		ErrWriter:     nopWriteCloser{&bytes.Buffer{}},
	}
	l = l.Go(ctx)

	var opts docker.LogsOptions
	select {
	case opts = <-reopened:
	case <-time.After(5 * time.Second):
		t.Fatal("dropped stream was not reopened")
	}
	assert.Empty(t, opts.Tail, "reopen should not repeat the last lines")
	assert.GreaterOrEqual(t, opts.Since, start, "reopen should continue from the last line written")
	assert.LessOrEqual(t, opts.Since, time.Now().Unix())

	first := mock.LogsCalls()[0].LogsOptions
	assert.Equal(t, "10", first.Tail)
	assert.Zero(t, first.Since)

	cancel()
	l.Wait()
	require.NoError(t, l.Err())
	assert.Equal(t, "before drop\nafter drop\n", logBuf.String())
}

func TestLogStreamer_CancelDuringReopenDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	called := make(chan struct{}, 1)
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		select {
		case called <- struct{}{}:
		default:
		}
		return io.ErrUnexpectedEOF
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("Logs was not called")
	}

	cancel()
	waitStart := time.Now()
	l.Wait()
	assert.Less(t, time.Since(waitStart), minReopenDelay, "cancel should not wait out the reopen delay")
	assert.Len(t, mock.LogsCalls(), 1)
	assert.NoError(t, l.Err())
}

func TestReopenable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection dropped mid-stream", err: io.ErrUnexpectedEOF, want: true},
		{name: "moby empty log", err: errors.New("error from daemon in stream: Error grabbing logs: EOF"), want: true},
		{name: "connection refused", err: docker.ErrConnectionRefused, want: true},
		{name: "inactivity timeout", err: docker.ErrInactivityTimeout, want: true},
		{name: "proxy bad gateway", err: &docker.Error{Status: http.StatusBadGateway}, want: true},
		{name: "daemon internal error", err: &docker.Error{Status: http.StatusInternalServerError}, want: true},
		{name: "no such container", err: &docker.Error{Status: http.StatusNotFound}, want: false},
		{name: "forbidden by proxy", err: &docker.Error{Status: http.StatusForbidden}, want: false},
		{name: "log driver can't be read", err: &docker.Error{Status: http.StatusNotImplemented}, want: false},
		{name: "empty container id", err: &docker.NoSuchContainer{}, want: false},
		{name: "wrapped refusal", err: fmt.Errorf("logs: %w", &docker.Error{Status: http.StatusNotFound}), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reopenable(tt.err))
		})
	}
}

func TestTrackWrites(t *testing.T) {
	var last atomic.Int64
	buf := &bytes.Buffer{}
	before := time.Now().Unix()
	n, err := trackWrites(buf, &last).Write([]byte("line\n"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "line\n", buf.String())
	assert.GreaterOrEqual(t, last.Load(), before)

	last.Store(1)
	_, err = trackWrites(failingWriter{}, &last).Write([]byte("lost\n"))
	require.Error(t, err)
	assert.Equal(t, int64(1), last.Load(), "a failed write should not move the reopen point past the lost line")

	assert.Nil(t, trackWrites(nil, &last))
}

func TestLogStreamer_WritesToOutputStreams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logBuf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}

	wrote := make(chan struct{})
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		_, _ = opts.OutputStream.Write([]byte("stdout line\n"))
		_, _ = opts.ErrorStream.Write([]byte("stderr line\n"))
		close(wrote)
		return nil
	}}

	l := &LogStreamer{
		ContainerID:   "test_id",
		ContainerName: "test_name",
		DockerClient:  mock,
		LogWriter:     nopWriteCloser{logBuf},
		ErrWriter:     nopWriteCloser{errBuf},
	}
	l.Go(ctx)
	<-wrote // wait for mock to finish writing

	assert.Equal(t, "stdout line\n", logBuf.String())
	assert.Equal(t, "stderr line\n", errBuf.String())
	cancel()
}

func TestLogStreamer_InitialTail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		assert.Equal(t, "10", opts.Tail, "initial tail should be 10")
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)
	require.Eventually(t, func() bool { return len(mock.LogsCalls()) >= 1 },
		5*time.Second, 10*time.Millisecond, "should have called Logs")
	cancel()
	l.Wait()
}

func TestLogStreamer_GoReturnsPointer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	result := l.Go(ctx)
	require.NotNil(t, result)
	assert.Equal(t, "test_id", result.ContainerID)
	cancel()
	result.Wait()
}

func TestLogStreamer_ErrAfterError(t *testing.T) {
	ctx := context.Background()
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		return &docker.Error{Status: http.StatusNotFound, Message: "No such container: test_id"}
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)

	l.Wait()
	require.EqualError(t, l.Err(), "API error (404): No such container: test_id")
	l.Close()
}

func TestLogStreamer_ErrAfterNormalClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := &mocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
		<-opts.Context.Done()
		return opts.Context.Err()
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)

	require.Eventually(t, func() bool { return len(mock.LogsCalls()) >= 1 },
		5*time.Second, 10*time.Millisecond, "should have called Logs")
	cancel()
	l.Wait()
	assert.NoError(t, l.Err(), "no error expected after normal context cancellation")
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// eventWriter records writes and Close in the order they happen
type eventWriter struct {
	mu  sync.Mutex
	log []string
}

func (w *eventWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = append(w.log, "write "+string(p))
	return len(p), nil
}

func (w *eventWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.log = append(w.log, "close")
	return nil
}

func (w *eventWriter) events() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.log...)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
