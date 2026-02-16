package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/docker-logger/app/discovery"
	logmocks "github.com/umputun/docker-logger/app/logger/mocks"
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

	time.Sleep(200 * time.Millisecond) // let it start
}

func Test_doValidation(t *testing.T) {
	tests := []struct {
		name string
		opts cliOpts
		err  string
	}{
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
			runEventLoop(ctx, &opts, eventsCh, mockClient)
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
		time.Sleep(100 * time.Millisecond)

		cancel()
		<-done
	})

	t.Run("duplicate start ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)

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
			runEventLoop(ctx, &opts, eventsCh, mockClient)
			close(done)
		}()

		// send same container start event twice
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Status: true}
		time.Sleep(50 * time.Millisecond)
		eventsCh <- discovery.Event{ContainerID: "c1", ContainerName: "test1", Status: true}
		time.Sleep(50 * time.Millisecond)

		// only one log stream should have been created
		assert.Equal(t, int32(1), logsCalls.Load(), "duplicate start should be ignored")

		cancel()
		<-done
	})

	t.Run("stop non-mapped container ignored", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			runEventLoop(ctx, &opts, eventsCh, mockClient)
			close(done)
		}()

		// send stop event for non-existing container, should not panic or error
		eventsCh <- discovery.Event{ContainerID: "unknown", ContainerName: "unknown", Status: false}
		time.Sleep(50 * time.Millisecond)

		assert.Empty(t, mockClient.LogsCalls(), "no log streams should be created for stop-only events")

		cancel()
		<-done
	})

	t.Run("graceful shutdown closes all streams", func(t *testing.T) {
		tmpDir := t.TempDir()
		opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
		eventsCh := make(chan discovery.Event, 10)

		ctx, cancel := context.WithCancel(context.Background())

		var logsCalls atomic.Int32
		mockClient := &logmocks.LogClientMock{LogsFunc: func(opts docker.LogsOptions) error {
			logsCalls.Add(1)
			<-opts.Context.Done()
			return opts.Context.Err()
		}}

		done := make(chan struct{})
		go func() {
			runEventLoop(ctx, &opts, eventsCh, mockClient)
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
}

func Test_makeLogWriters(t *testing.T) {
	tmpDir := t.TempDir()
	setupLog(true)

	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
	stdWr, errWr := makeLogWriters(&opts, "container1", "gr1")
	assert.NotEqual(t, stdWr, errWr, "different writers for out and err")

	_, err := stdWr.Write([]byte("abc line 1\n"))
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
	stdWr, errWr := makeLogWriters(&opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in mixed mode")

	_, err := stdWr.Write([]byte("abc line 1\n"))
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
	assert.Equal(t, "abc line 1\nxxx123 line 2\nerr line 1\nxxx123 line 2\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersWithJSON(t *testing.T) {
	tmpDir := t.TempDir()
	opts := cliOpts{FilesLocation: tmpDir, EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, ExtJSON: true}
	stdWr, errWr := makeLogWriters(&opts, "container1", "gr1")

	_, err := stdWr.Write([]byte("abc line 1"))
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
	stdWr, errWr := makeLogWriters(&opts, "container1", "")

	_, err := stdWr.Write([]byte("test line\n"))
	require.NoError(t, err)

	logFile := filepath.Join(tmpDir, "container1.log")
	r, err := os.ReadFile(logFile) //nolint:gosec // test file path
	require.NoError(t, err)
	assert.Equal(t, "test line\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersSyslogFailed(t *testing.T) {
	opts := cliOpts{EnableSyslog: true}
	stdWr, errWr := makeLogWriters(&opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in syslog")

	_, err := stdWr.Write([]byte("abc line 1\n"))
	require.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	_, err = errWr.Write([]byte("err line 1\n"))
	require.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)
}

func Test_makeLogWritersSyslogPassed(t *testing.T) {
	opts := cliOpts{EnableSyslog: true, SyslogHost: "127.0.0.1:514", SyslogPrefix: "docker/"}
	stdWr, errWr := makeLogWriters(&opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in syslog")

	_, err := stdWr.Write([]byte("abc line 1\n"))
	require.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)

	_, err = errWr.Write([]byte("err line 1\n"))
	require.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	require.NoError(t, err)
}
