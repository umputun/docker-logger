package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/docker-logger/app/discovery"
	"github.com/umputun/docker-logger/app/discovery/mocks"
	logmocks "github.com/umputun/docker-logger/app/logger/mocks"
	"github.com/umputun/docker-logger/app/syslog"
)

func Test_Do(t *testing.T) {
	if os.Getenv("TEST_DOCKER") == "" {
		t.Skip("skip docker tests")
	}

	tmpDir := t.TempDir()
	opts := cliOpts{
		DockerHost:    "unix:///var/run/docker.sock",
		FilesLocation: tmpDir,
		EnableFiles:   true,
		MaxFileSize:   1,
		MaxFilesCount: 10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*500)
	defer cancel()
	err := do(ctx, &opts)
	require.NoError(t, err)
}

func Test_doValidation(t *testing.T) {
	tests := []struct {
		name string
		opts cliOpts
		err  string
	}{
		{name: "no destinations enabled",
			opts: cliOpts{},
			err:  "at least one log destination must be enabled"},
		{name: "includes and includesPattern conflict",
			opts: cliOpts{Includes: []string{"foo"}, IncludesPattern: "bar.*", EnableFiles: true},
			err:  "only single option Includes/IncludesPattern are allowed"},
		{name: "invalid includesPattern",
			opts: cliOpts{DockerHost: "unix:///var/run/docker.sock", IncludesPattern: "[invalid", EnableFiles: true},
			err:  "failed to compile includesPattern"},
		{name: "includes and excludes conflict",
			opts: cliOpts{Includes: []string{"foo"}, Excludes: []string{"bar"}, EnableFiles: true},
			err:  "only single option Excludes/Includes are allowed"},
		{name: "excludes and excludesPattern conflict",
			opts: cliOpts{Excludes: []string{"foo"}, ExcludesPattern: "bar.*", EnableFiles: true},
			err:  "only single option Excludes/ExcludesPattern are allowed"},
		{name: "includesPattern and excludesPattern conflict",
			opts: cliOpts{IncludesPattern: "foo.*", ExcludesPattern: "bar.*", EnableFiles: true},
			err:  "only single option IncludesPattern/ExcludesPattern are allowed"},
		{name: "includes and excludesPattern conflict",
			opts: cliOpts{Includes: []string{"foo"}, ExcludesPattern: "bar.*", EnableFiles: true},
			err:  "only single option Includes/ExcludesPattern are allowed"},
		{name: "includesPattern and excludes conflict",
			opts: cliOpts{IncludesPattern: "foo.*", Excludes: []string{"bar"}, EnableFiles: true},
			err:  "only single option IncludesPattern/Excludes are allowed"},
		{name: "invalid excludesPattern",
			opts: cliOpts{DockerHost: "unix:///var/run/docker.sock", ExcludesPattern: "[invalid", EnableFiles: true},
			err:  "failed to compile excludesPattern"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := do(t.Context(), &tt.opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.err)
		})
	}
}

func Test_doInvalidDockerHost(t *testing.T) {
	opts := cliOpts{DockerHost: "invalid-scheme://host", EnableFiles: true}
	err := do(t.Context(), &opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to make docker client")
}

func Test_runEventLoop(t *testing.T) {
	t.Run("start and stop container", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			if opts.OutputStream != nil {
				_, _ = opts.OutputStream.Write([]byte("started\n"))
			}
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, eventsCh, listenerErr, mockClient)
			close(done)
		}()

		// send start event
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Group: "gr1", Status: true}
		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Join(tmpDir, "gr1", "test1.log"))
			return err == nil
		}, time.Second, 10*time.Millisecond, "log file should be created")

		// send stop event
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Group: "gr1", Status: false}

		// send a sentinel start event for a different container; when it's processed we know stop was handled
		eventsCh <- discovery.Event{ContainerID: "c2", ContainerName: "test2", Group: "gr1", Status: true}
		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Join(tmpDir, "gr1", "test2.log"))
			return err == nil
		}, time.Second, 10*time.Millisecond, "sentinel container should be processed, confirming stop was handled")

		cancel()
		<-done
	})

	t.Run("duplicate start ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var logsCalls atomic.Int32
		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			logsCalls.Add(1)
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, eventsCh, listenerErr, mockClient)
			close(done)
		}()

		// send same container start event twice
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Status: true}
		require.Eventually(t, func() bool { return logsCalls.Load() == 1 },
			time.Second, 10*time.Millisecond, "first event should be processed")
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Status: true}

		// send a sentinel start for a different container to confirm the duplicate was processed
		eventsCh <- discovery.Event{ContainerID: "c-sentinel", ContainerName: "sentinel", Status: true}
		require.Eventually(t, func() bool { return logsCalls.Load() == 2 },
			time.Second, 10*time.Millisecond, "sentinel should be processed, confirming duplicate was handled")

		// only two log streams should have been created (c1 + sentinel, not c1 twice)
		assert.Equal(t, int32(2), logsCalls.Load(), "duplicate start should be ignored")

		cancel()
		<-done
	})

	t.Run("stop non-mapped container ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, eventsCh, listenerErr, mockClient)
			close(done)
		}()

		// send stop event for non-existing container, should not panic or error
		eventsCh <- discovery.Event{ContainerID: "unknown", ContainerName: "unknown", Status: false}

		// send a sentinel start event to confirm the stop event was processed
		eventsCh <- discovery.Event{ContainerID: "c-sentinel", ContainerName: "sentinel", Status: true}
		require.Eventually(t, func() bool { return len(mockClient.LogsCalls()) == 1 },
			time.Second, 10*time.Millisecond, "sentinel should be processed, confirming stop was handled")

		// only one log stream should exist (sentinel), confirming stop didn't create any
		assert.Len(t, mockClient.LogsCalls(), 1, "only sentinel log stream should be created")

		cancel()
		<-done
	})

	t.Run("graceful shutdown closes all streams", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		ctx, cancel := context.WithCancel(context.Background())

		var logsCalls atomic.Int32
		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			logsCalls.Add(1)
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, eventsCh, listenerErr, mockClient)
			close(done)
		}()

		// start 3 containers
		for _, id := range []string{"c1", "c2", "c3"} {
			eventsCh <- discovery.Event{ContainerID: id, ContainerName: id, Status: true}
		}

		require.Eventually(t, func() bool { return logsCalls.Load() == 3 },
			time.Second, 10*time.Millisecond, "all 3 containers should be streaming")

		cancel()
		<-done // runEventLoop should close all streams and return
	})

	t.Run("mixErr mode closes writer once", func(t *testing.T) {
		// verify that with MixErr=true, the err writer is not separately closed on container stop.
		// both stdout and stderr data should end up in the same .log file and no .err file should exist.
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, MixErr: true}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			if opts.OutputStream != nil {
				_, _ = opts.OutputStream.Write([]byte("stdout line\n"))
			}
			if opts.ErrorStream != nil {
				_, _ = opts.ErrorStream.Write([]byte("stderr line\n"))
			}
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, eventsCh, listenerErr, mockClient)
			close(done)
		}()

		// start container
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Group: "gr1", Status: true}
		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Join(tmpDir, "gr1", "test1.log"))
			return err == nil
		}, time.Second, 10*time.Millisecond, "log file should be created")

		// stop the container — this should close LogWriter but skip closing ErrWriter
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Group: "gr1", Status: false}

		// send sentinel to confirm stop was processed
		eventsCh <- discovery.Event{ContainerID: "c2", ContainerName: "sentinel", Group: "gr1", Status: true}
		require.Eventually(t, func() bool {
			_, err := os.Stat(filepath.Join(tmpDir, "gr1", "sentinel.log"))
			return err == nil
		}, time.Second, 10*time.Millisecond, "sentinel container should be processed")

		// verify both stdout and stderr ended up in the same .log file
		logFile := filepath.Join(tmpDir, "gr1", "test1.log")
		data, err := os.ReadFile(logFile) //nolint:gosec // test file path
		require.NoError(t, err)
		assert.Contains(t, string(data), "stdout line")
		assert.Contains(t, string(data), "stderr line")

		// verify no .err file was created
		_, err = os.Stat(filepath.Join(tmpDir, "gr1", "test1.err"))
		assert.True(t, os.IsNotExist(err), ".err file should not exist in MixErr mode")

		cancel()
		<-done
	})

	t.Run("closed events channel exits loop with error", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		errCh := make(chan error, 1)
		go func() {
			errCh <- runEventLoop(context.Background(), &opts, eventsCh, listenerErr, mockClient)
		}()

		// close events channel to simulate EventNotif failure
		close(eventsCh)

		select {
		case err := <-errCh:
			require.Error(t, err, "runEventLoop should return error when events channel closed")
			assert.Contains(t, err.Error(), "events channel closed unexpectedly")
		case <-time.After(5 * time.Second):
			t.Fatal("runEventLoop should exit after events channel closed")
		}
	})

	t.Run("listener error propagated", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		errCh := make(chan error, 1)
		go func() {
			errCh <- runEventLoop(context.Background(), &opts, eventsCh, listenerErr, mockClient)
		}()

		// simulate listener failure
		listenerErr <- errors.New("can't add event listener: connection refused")

		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.Contains(t, err.Error(), "connection refused")
		case <-time.After(5 * time.Second):
			t.Fatal("runEventLoop should exit after listener error")
		}
	})

	t.Run("listener error preferred over generic channel close", func(t *testing.T) {
		// simulates the real activate() pattern: error sent to listenerErr AND eventsCh closed.
		// regardless of which select case fires first, the listener error should be returned.
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)
		listenerErr := make(chan error, 1)

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		// send error and close channel before starting the loop, so both are ready
		listenerErr <- errors.New("can't add event listener: connection refused")
		close(eventsCh)

		errCh := make(chan error, 1)
		go func() {
			errCh <- runEventLoop(context.Background(), &opts, eventsCh, listenerErr, mockClient)
		}()

		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.Contains(t, err.Error(), "connection refused",
				"should return the listener error, not generic 'events channel closed'")
		case <-time.After(5 * time.Second):
			t.Fatal("runEventLoop should exit after listener error with closed channel")
		}
	})
}

func Test_makeLogWriters(t *testing.T) {
	tmpDir := t.TempDir()
	setupLog(true)

	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)
	assert.NotEqual(t, stdWr, errWr, "different writers for out and err")

	_, err = stdWr.Write([]byte("abc line 1\n"))
	require.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	_, err = errWr.Write([]byte("err line 1\n"))
	require.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	logFile := filepath.Join(tmpDir, "gr1", "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "abc line 1\nxxx123 line 2\n", string(r))

	errFile := filepath.Join(tmpDir, "gr1", "container1.err")
	r, err = os.ReadFile(errFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "err line 1\nxxx123 line 2\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersMixed(t *testing.T) {
	tmpDir := t.TempDir()
	setupLog(false)

	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, MixErr: true}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)
	assert.NotNil(t, stdWr, "log writer should not be nil")
	assert.NotNil(t, errWr, "err writer should not be nil")

	// write to both log and err writers
	_, err = stdWr.Write([]byte("abc line 1\n"))
	require.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	_, err = errWr.Write([]byte("err line 1\n"))
	require.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	// verify all data ends up in the single .log file
	logFile := filepath.Join(tmpDir, "gr1", "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "abc line 1\nxxx123 line 2\nerr line 1\nxxx123 line 2\n", string(r))

	// verify no .err file was created in mixed mode
	_, err = os.Stat(filepath.Join(tmpDir, "gr1", "container1.err"))
	assert.True(t, os.IsNotExist(err), ".err file should not exist in mixed mode")

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersWithJSON(t *testing.T) {
	tmpDir := t.TempDir()
	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, ExtJSON: true}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)

	_, err = stdWr.Write([]byte("abc line 1"))
	require.NoError(t, err)

	logFile := filepath.Join(tmpDir, "gr1", "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Contains(t, string(r), `"msg":"abc line 1","container":"container1","group":"gr1"`)

	_, err = os.Stat(filepath.Join(tmpDir, "gr1", "container1.err"))
	require.Error(t, err)

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersNoGroup(t *testing.T) {
	tmpDir := t.TempDir()
	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "")
	require.NoError(t, err)

	_, err = stdWr.Write([]byte("test line\n"))
	require.NoError(t, err)

	logFile := filepath.Join(tmpDir, "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "test line\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersNeitherEnabled(t *testing.T) {
	opts := cliOpts{}
	_, _, err := makeLogWriters(&opts, "container1", "gr1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either files or syslog has to be enabled")
}

func Test_makeLogWritersInvalidDir(t *testing.T) {
	// create a regular file, then use a path under it as FilesLocation to guarantee mkdir failure
	invalidParent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(invalidParent, []byte("x"), 0o600))

	opts := cliOpts{EnableFiles: true, FilesLocation: filepath.Join(invalidParent, "subdir")}
	_, _, err := makeLogWriters(&opts, "container1", "gr1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't make directory")
}

func Test_makeLogWritersFilesAndSyslogNoDoubleClose(t *testing.T) {
	if !syslog.IsSupported() {
		t.Skip("syslog not supported on this platform")
	}
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()

	tmpDir := t.TempDir()
	opts := cliOpts{
		FilesLocation: tmpDir, EnableFiles: true, EnableSyslog: true,
		SyslogHost: conn.LocalAddr().String(), SyslogPrefix: "docker/",
		MaxFileSize: 1, MaxFilesCount: 10,
	}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)

	// write to both writers
	_, err = stdWr.Write([]byte("log line\n"))
	require.NoError(t, err)
	_, err = errWr.Write([]byte("err line\n"))
	require.NoError(t, err)

	// close both writers without double-close errors
	assert.NoError(t, stdWr.Close(), "log writer close should not error")
	assert.NoError(t, errWr.Close(), "err writer close should not error")
}

func Test_writeNopCloser(t *testing.T) {
	var closeCalls int
	mock := &mockWriteCloser{
		writeFunc: func(p []byte) (int, error) { return len(p), nil },
		closeFunc: func() error { closeCalls++; return nil },
	}

	// writeNopCloser should write through but not close
	nop := writeNopCloser{mock}
	_, err := nop.Write([]byte("test"))
	require.NoError(t, err)
	assert.NoError(t, nop.Close())
	assert.Equal(t, 0, closeCalls, "underlying writer should not be closed via nop closer")

	// direct close should work
	require.NoError(t, mock.Close())
	assert.Equal(t, 1, closeCalls, "underlying writer should be closed once via direct call")
}

type mockWriteCloser struct {
	writeFunc func(p []byte) (int, error)
	closeFunc func() error
}

func (m *mockWriteCloser) Write(p []byte) (int, error) { return m.writeFunc(p) }
func (m *mockWriteCloser) Close() error                { return m.closeFunc() }

func Test_makeLogWritersSyslogFailedNoFiles(t *testing.T) {
	if !syslog.IsSupported() {
		t.Skip("syslog not supported on this platform")
	}
	// syslog-only mode with invalid host should return error, not create empty writers
	opts := cliOpts{EnableSyslog: true, SyslogHost: "invalid:::host"}
	_, _, err := makeLogWriters(&opts, "container1", "gr1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no log destinations available")
}

func Test_makeLogWritersSyslogOnly(t *testing.T) {
	if !syslog.IsSupported() {
		t.Skip("syslog not supported on this platform")
	}
	// syslog-only mode (files disabled), syslog connects to a real UDP listener
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()

	opts := cliOpts{EnableSyslog: true, SyslogHost: conn.LocalAddr().String(), SyslogPrefix: "docker/"}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)
	assert.NotEqual(t, stdWr, errWr, "err writer wraps syslog with nop closer")

	_, err = stdWr.Write([]byte("syslog test message\n"))
	require.NoError(t, err)

	// verify data arrives at the UDP listener
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf[:n]), "syslog test message")
	assert.Contains(t, string(buf[:n]), "docker/container1")

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersSyslogWithFiles(t *testing.T) {
	if !syslog.IsSupported() {
		t.Skip("syslog not supported on this platform")
	}
	// both files and syslog enabled, verify syslog data arrives and files are written
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer conn.Close()

	tmpDir := t.TempDir()
	opts := cliOpts{
		EnableFiles: true, FilesLocation: tmpDir, MaxFileSize: 1, MaxFilesCount: 10,
		EnableSyslog: true, SyslogHost: conn.LocalAddr().String(), SyslogPrefix: "docker/",
	}
	stdWr, errWr, err := makeLogWriters(&opts, "container1", "gr1")
	require.NoError(t, err)

	_, err = stdWr.Write([]byte("log message\n"))
	require.NoError(t, err)

	// verify syslog received the data
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFrom(buf)
	require.NoError(t, err)
	assert.Contains(t, string(buf[:n]), "log message")

	// verify file was also written
	logFile := filepath.Join(tmpDir, "gr1", "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "log message\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

// fakeDaemon stands in for the docker daemon behind both discovery and the log streamers. It keeps
// the history of container events, replays it from Since on subscription as the daemon does, and
// follows a container's log until that container stops.
type fakeDaemon struct {
	mu       sync.Mutex
	history  []*docker.APIEvents
	runs     map[string]chan struct{} // closed when the container's current run stops, absent while stopped
	active   map[string]int           // Logs calls following a running container
	exited   chan string              // id of the container whose streamer has exited or been closed
	listener chan<- *docker.APIEvents
	lastNano int64
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{runs: map[string]chan struct{}{}, active: map[string]int{}, exited: make(chan string, 100)}
}

// record stamps each event later than the one before, as two transitions of one container never share a stamp
func (d *fakeDaemon) record(id, status string) *docker.APIEvents {
	d.lastNano = max(time.Now().UnixNano(), d.lastNano+1)
	ev := &docker.APIEvents{Type: "container", Status: status, Time: d.lastNano / 1e9, TimeNano: d.lastNano}
	ev.Actor.ID = id
	ev.Actor.Attributes = map[string]string{"name": "name-" + id}
	d.history = append(d.history, ev)
	return ev
}

func (d *fakeDaemon) start(id string) *docker.APIEvents {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.runs[id] = make(chan struct{})
	return d.record(id, "start")
}

func (d *fakeDaemon) stop(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	close(d.runs[id])
	delete(d.runs, id)
	d.record(id, "die")
}

// container makes a listing entry created well before any of its events
func (d *fakeDaemon) container(id string) docker.APIContainers {
	return docker.APIContainers{ID: id, Names: []string{"/name-" + id}, Created: time.Now().Add(-time.Hour).Unix()}
}

// replay returns the recorded events stamped at or after since, "seconds[.nanoseconds]" as the daemon
// takes it. Without since the daemon replays nothing.
func (d *fakeDaemon) replay(t *testing.T, since string) []*docker.APIEvents {
	if since == "" {
		return nil
	}
	secs, nsecs, _ := strings.Cut(since, ".")
	sec, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		t.Errorf("invalid since %q, %v", since, err)
	}
	var nsec int64
	if nsecs != "" {
		if nsec, err = strconv.ParseInt((nsecs + "000000000")[:9], 10, 64); err != nil {
			t.Errorf("invalid since %q, %v", since, err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var res []*docker.APIEvents
	for _, ev := range d.history {
		if ev.TimeNano >= time.Unix(sec, nsec).UnixNano() {
			res = append(res, ev)
		}
	}
	return res
}

func (d *fakeDaemon) Logs(opts docker.LogsOptions) error {
	d.mu.Lock()
	run, running := d.runs[opts.Container]
	if running {
		d.active[opts.Container]++
	}
	d.mu.Unlock()
	// the streamer cancels its context only after it has exited, or when it is closed
	go func() {
		<-opts.Context.Done()
		d.exited <- opts.Container
	}()
	if !running {
		return nil // the daemon ends the stream of a stopped container at once
	}
	defer func() {
		d.mu.Lock()
		d.active[opts.Container]--
		d.mu.Unlock()
	}()
	select {
	case <-run:
		return nil
	case <-opts.Context.Done():
		return opts.Context.Err()
	}
}

func (d *fakeDaemon) following(id string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active[id]
}

// waitExited waits for a streamer of the container to exit
func (d *fakeDaemon) waitExited(t *testing.T, id string) {
	for {
		select {
		case got := <-d.exited:
			if got == id {
				return
			}
		case <-time.After(5 * time.Second):
			t.Errorf("log stream of %s did not end", id)
			return
		}
	}
}

func Test_runEventLoopReplayedEvents(t *testing.T) {
	// run starts discovery and the event loop on the fake daemon. scan runs inside the container listing,
	// after since is taken, and returns the listing. subscribe runs when the event listener is added,
	// before the daemon replays the events since, and order returns them in the order they are delivered.
	run := func(t *testing.T, d *fakeDaemon, scan func() []docker.APIContainers, subscribe func(),
		order func(events []*docker.APIEvents) []*docker.APIEvents) {
		t.Helper()
		replayed := make(chan struct{})
		dockerClient := &mocks.DockerClientMock{
			ListContainersFunc: func(docker.ListContainersOptions) ([]docker.APIContainers, error) {
				return scan(), nil
			},
			AddEventListenerWithOptionsFunc: func(opts docker.EventsOptions, listener chan<- *docker.APIEvents) error {
				subscribe()
				events := order(d.replay(t, opts.Since))
				go func() {
					defer close(replayed)
					for _, ev := range events {
						listener <- ev
					}
					d.mu.Lock()
					d.listener = listener
					d.mu.Unlock()
				}()
				return nil
			},
		}
		notif, err := discovery.NewEventNotif(dockerClient, discovery.EventNotifOpts{})
		require.NoError(t, err)

		opts := cliOpts{FilesLocation: t.TempDir(), EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			_ = runEventLoop(ctx, &opts, notif.Channel(), notif.Err(), d)
			close(done)
		}()
		t.Cleanup(func() {
			cancel()
			<-done
			// closing the listener ends discovery, which closes its channel
			<-replayed
			close(d.listener)
			for range notif.Channel() {
			}
		})
	}

	// live sends an event once the replay is delivered
	live := func(t *testing.T, d *fakeDaemon, ev *docker.APIEvents) {
		t.Helper()
		var listener chan<- *docker.APIEvents
		require.Eventually(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			listener = d.listener
			return listener != nil
		}, 5*time.Second, time.Millisecond, "replay should be delivered")
		listener <- ev
	}

	// settled checks the container is followed by exactly one stream once every event sent so far is
	// processed, which a start of a sentinel container sent after them confirms
	settled := func(t *testing.T, d *fakeDaemon, id string) {
		t.Helper()
		live(t, d, d.start("sentinel"))
		require.Eventually(t, func() bool { return d.following("sentinel") == 1 }, 5*time.Second, time.Millisecond)
		require.Eventually(t, func() bool { return d.following(id) == 1 }, 5*time.Second, time.Millisecond,
			"%s should be followed by one stream, got %d", id, d.following(id))
		require.Never(t, func() bool { return d.following(id) != 1 }, 100*time.Millisecond, time.Millisecond)
	}

	inOrder := func(events []*docker.APIEvents) []*docker.APIEvents { return events }
	reversed := func(events []*docker.APIEvents) []*docker.APIEvents {
		res := slices.Clone(events)
		slices.Reverse(res)
		return res
	}

	t.Run("missed start", func(t *testing.T) {
		d := newFakeDaemon()
		run(t, d, func() []docker.APIContainers {
			d.start("c1") // after the listing is composed, so it is not in it
			return nil
		}, func() {}, inOrder)
		settled(t, d, "c1")
	})

	t.Run("missed stop with the same id returning", func(t *testing.T) {
		d := newFakeDaemon()
		d.start("c1")
		run(t, d, func() []docker.APIContainers {
			c1 := d.container("c1")
			d.stop("c1") // after the listing is composed, so it still lists the container
			return []docker.APIContainers{c1}
		}, func() {}, inOrder)
		d.waitExited(t, "c1")
		live(t, d, d.start("c1"))
		settled(t, d, "c1")
	})

	// the listing still has the container stopped after it was composed, so the streamer made for
	// that entry exits at once. The container starts again before the replay is delivered.
	for _, tc := range []struct {
		name  string
		order func([]*docker.APIEvents) []*docker.APIEvents
	}{{"stop then start on a stale entry", inOrder}, {"start then stop on a stale entry", reversed}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon()
			d.start("c1")
			run(t, d, func() []docker.APIContainers {
				c1 := d.container("c1")
				d.stop("c1")
				return []docker.APIContainers{c1}
			}, func() {
				d.waitExited(t, "c1")
				d.start("c1")
			}, tc.order)
			settled(t, d, "c1")
		})
	}

	// the container stops and starts again before the listing is composed, so the streamer made for
	// its entry follows the new run
	for _, tc := range []struct {
		name  string
		order func([]*docker.APIEvents) []*docker.APIEvents
	}{{"stop then start on a live entry", inOrder}, {"start then stop on a live entry", reversed}} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeDaemon()
			d.start("c1")
			run(t, d, func() []docker.APIContainers {
				d.stop("c1")
				d.start("c1")
				return []docker.APIContainers{d.container("c1")}
			}, func() {
				assert.Eventually(t, func() bool { return d.following("c1") == 1 }, 5*time.Second, time.Millisecond)
			}, tc.order)
			settled(t, d, "c1")
		})
	}
}
