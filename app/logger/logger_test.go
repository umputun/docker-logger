package logger

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	st := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	l.Wait()
	assert.Less(t, time.Since(st), time.Second, "should terminate quickly after cancel")
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

	go func() {
		time.Sleep(100 * time.Millisecond)
		l.Close()
	}()
	st := time.Now()
	l.Wait()
	assert.Less(t, time.Since(st), time.Second, "should terminate after close")
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
	time.Sleep(50 * time.Millisecond)
	l.Close()
	assert.Len(t, mock.LogsCalls(), 1)
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
		return errors.New("some docker error")
	}}

	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: mock}
	l = l.Go(ctx)

	time.Sleep(50 * time.Millisecond)
	l.Close()
	assert.Len(t, mock.LogsCalls(), 1)
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
	time.Sleep(50 * time.Millisecond)
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

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
