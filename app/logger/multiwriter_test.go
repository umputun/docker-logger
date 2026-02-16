package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMultiWriter_Write(t *testing.T) {
	// with ext JSON
	w1, w2 := wrMock{}, wrMock{}
	writer := NewMultiWriterIgnoreErrors(&w1, &w2).WithExtJSON("c1", "g1")
	n, err := writer.Write([]byte("test 123"))
	require.NoError(t, err)
	assert.Equal(t, 8, n)

	s1, s2 := w1.String(), w2.String()
	assert.Equal(t, s1, s2, "both dest writers have the same data")
	assert.True(t, strings.HasPrefix(w1.String(), `{"msg":"test 123"`))
	t.Log(s1)

	// without ext JSON
	w1, w2 = wrMock{}, wrMock{}
	writer = NewMultiWriterIgnoreErrors(&w1, &w2)
	n, err = writer.Write([]byte("test 123"))
	require.NoError(t, err)
	assert.Equal(t, 8, n)
	assert.Equal(t, "test 123", w1.String())
	assert.Equal(t, "test 123", w2.String())
}

func TestMultiWriter_WritePartialFailure(t *testing.T) {
	good := &wrMock{}
	bad := &errWriteCloser{writeErr: errors.New("write failed")}
	writer := NewMultiWriterIgnoreErrors(good, bad)

	n, err := writer.Write([]byte("test data"))
	require.NoError(t, err, "partial failure should not return error")
	assert.Equal(t, 9, n)
	assert.Equal(t, "test data", good.String())
}

func TestMultiWriter_WriteAllFailed(t *testing.T) {
	bad1 := &errWriteCloser{writeErr: errors.New("write failed 1")}
	bad2 := &errWriteCloser{writeErr: errors.New("write failed 2")}
	writer := NewMultiWriterIgnoreErrors(bad1, bad2)

	n, err := writer.Write([]byte("test data"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all writers failed")
	assert.Equal(t, 9, n)
}

func TestMultiWriter_WriteNoWriters(t *testing.T) {
	writer := NewMultiWriterIgnoreErrors()
	n, err := writer.Write([]byte("test data"))
	// zero writers, zero errors; errors.Wrap(nil, ...) returns nil
	require.NoError(t, err)
	assert.Equal(t, 9, n)
}

func TestMultiWriter_Close(t *testing.T) {
	t.Run("all writers close successfully", func(t *testing.T) {
		w1, w2 := &wrMock{}, &wrMock{}
		writer := NewMultiWriterIgnoreErrors(w1, w2)
		err := writer.Close()
		require.NoError(t, err)
	})

	t.Run("one writer fails to close", func(t *testing.T) {
		good := &wrMock{}
		bad := &errWriteCloser{closeErr: errors.New("close failed")}
		writer := NewMultiWriterIgnoreErrors(good, bad)
		err := writer.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close failed")
	})

	t.Run("multiple writers fail to close", func(t *testing.T) {
		bad1 := &errWriteCloser{closeErr: errors.New("close failed 1")}
		bad2 := &errWriteCloser{closeErr: errors.New("close failed 2")}
		writer := NewMultiWriterIgnoreErrors(bad1, bad2)
		err := writer.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close failed 1")
		assert.Contains(t, err.Error(), "close failed 2")
	})

	t.Run("no writers", func(t *testing.T) {
		writer := NewMultiWriterIgnoreErrors()
		err := writer.Close()
		require.NoError(t, err)
	})
}

func TestMultiWriter_extJSON(t *testing.T) {
	writer := NewMultiWriterIgnoreErrors().WithExtJSON("c1", "g1")
	res, err := writer.extJSON([]byte("test msg"))
	require.NoError(t, err)

	j := jMsg{}
	err = json.Unmarshal(res, &j)
	require.NoError(t, err)

	assert.Equal(t, "test msg", j.Msg)
	assert.Equal(t, "c1", j.Container)
	assert.Equal(t, "g1", j.Group)

	hname, err := os.Hostname()
	require.NoError(t, err)
	assert.Equal(t, hname, j.Host)
	assert.Less(t, time.Since(j.TS).Seconds(), float64(1))
}

func TestNewMultiWriterIgnoreErrors(t *testing.T) {
	w1, w2 := &wrMock{}, &wrMock{}
	mw := NewMultiWriterIgnoreErrors(w1, w2)
	assert.Len(t, mw.writers, 2)
	assert.False(t, mw.isJSON)
}

func TestMultiWriter_WithExtJSON(t *testing.T) {
	mw := NewMultiWriterIgnoreErrors().WithExtJSON("container1", "group1")
	assert.True(t, mw.isJSON)
	assert.Equal(t, "container1", mw.container)
	assert.Equal(t, "group1", mw.group)
	assert.NotEmpty(t, mw.hostname)
}

type wrMock struct {
	bytes.Buffer
}

func (m *wrMock) Close() error { return nil }

type errWriteCloser struct {
	writeErr error
	closeErr error
}

func (e *errWriteCloser) Write(p []byte) (int, error) {
	if e.writeErr != nil {
		return 0, e.writeErr
	}
	return len(p), nil
}

func (e *errWriteCloser) Close() error {
	return e.closeErr
}
