package main

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_makeLogWriters(t *testing.T) {
	defer os.RemoveAll("/tmp/logger.test")
	opts := cliOpts{FilesLocation: "/tmp/logger.test", EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")

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
	opts := cliOpts{FilesLocation: "/tmp/logger.test", EnableFiles: true, MaxFileSize: 1, MaxFilesCount: 10, MixErr: true}
	stdWr, errWr := makeLogWriters(opts, "container1", "gr1")

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
