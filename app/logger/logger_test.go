package logger

import (
	"bytes"
	"context"
	"errors"
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
