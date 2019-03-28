package main

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_Do(t *testing.T) {

	if os.Getenv("TEST_DOCKER") == "" {
		t.Skip("skip docker tests")
	}

	defer os.RemoveAll("/tmp/logger.test")
	opts := cliOpts{
		DockerHost:    "unix:///var/run/docker.sock",
		FilesLocation: "/tmp/logger.test",
		EnableFiles:   true,
		MaxFileSize:   1,
		MaxFilesCount: 10,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*500)
	defer cancel()
	err := do(ctx, opts)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond) // let it start
}

func Test_makeLogWriters(t *testing.T) {
	defer os.RemoveAll("/tmp/logger.test")
	setupLog(true)

	opts := cliOpts{FilesLocation: "/tmp/logger.test", EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")
	assert.NotEqual(t, stdWr, errWr, "different writers for out and err")

	// write to out writer
	_, err := stdWr.Write([]byte("abc line 1\n"))
	assert.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	// write to err writer
	_, err = errWr.Write([]byte("err line 1\n"))
	assert.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	r, err := ioutil.ReadFile("/tmp/logger.test/gr1/container1.log")
	assert.NoError(t, err)
	assert.Equal(t, "abc line 1\nxxx123 line 2\n", string(r))

	r, err = ioutil.ReadFile("/tmp/logger.test/gr1/container1.err")
	assert.NoError(t, err)
	assert.Equal(t, "err line 1\nxxx123 line 2\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersMixed(t *testing.T) {
	defer os.RemoveAll("/tmp/logger.test")
	setupLog(false)

	opts := cliOpts{FilesLocation: "/tmp/logger.test", EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, MixErr: true}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in mixed mode")

	// write to out writer
	_, err := stdWr.Write([]byte("abc line 1\n"))
	assert.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	// write to err writer
	_, err = errWr.Write([]byte("err line 1\n"))
	assert.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	r, err := ioutil.ReadFile("/tmp/logger.test/gr1/container1.log")
	assert.NoError(t, err)
	assert.Equal(t, "abc line 1\nxxx123 line 2\nerr line 1\nxxx123 line 2\n", string(r))

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersWithJSON(t *testing.T) {
	defer os.RemoveAll("/tmp/logger.test")
	opts := cliOpts{FilesLocation: "/tmp/logger.test", EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, ExtJSON: true}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")

	// write to out writer
	_, err := stdWr.Write([]byte("abc line 1"))
	assert.NoError(t, err)

	r, err := ioutil.ReadFile("/tmp/logger.test/gr1/container1.log")
	assert.NoError(t, err)
	assert.Contains(t, string(r), `"msg":"abc line 1","container":"container1","group":"gr1"`)

	_, err = os.Stat("/tmp/logger.test/gr1/container1.err")
	assert.NotNil(t, err)

	assert.NoError(t, stdWr.Close())
	assert.NoError(t, errWr.Close())
}

func Test_makeLogWritersSyslogFailed(t *testing.T) {
	opts := cliOpts{EnableSyslog: true}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in syslog")
	// write to out writer
	_, err := stdWr.Write([]byte("abc line 1\n"))
	assert.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	// write to err writer
	_, err = errWr.Write([]byte("err line 1\n"))
	assert.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)
}

func Test_makeLogWritersSyslogPassed(t *testing.T) {
	opts := cliOpts{EnableSyslog: true, SyslogHost: "127.0.0.1:514", SyslogPrefix: "docker/"}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")
	assert.Equal(t, stdWr, errWr, "same writer for out and err in syslog")

	// write to out writer
	_, err := stdWr.Write([]byte("abc line 1\n"))
	assert.NoError(t, err)
	_, err = stdWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)

	// write to err writer
	_, err = errWr.Write([]byte("err line 1\n"))
	assert.NoError(t, err)
	_, err = errWr.Write([]byte("xxx123 line 2\n"))
	assert.NoError(t, err)
}
