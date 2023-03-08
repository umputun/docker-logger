package logger

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
)

// MultiWriter implements WriteCloser for multiple destinations.
// It is simplified version of stdlib MultiWriter. Ignores write error and don't stop the loop unless all writes failed.
type MultiWriter struct {
	writers   []io.WriteCloser
	hostname  string
	container string
	group     string
	isJSON    bool
}

// jMsg is envelope for ExtJSON mode
type jMsg struct {
	Msg       string    `json:"msg"`
	Container string    `json:"container"`
	Group     string    `json:"group"`
	TS        time.Time `json:"ts"`
	Host      string    `json:"host"`
}

// NewMultiWriterIgnoreErrors create WriteCloser for multiple destinations
func NewMultiWriterIgnoreErrors(writers ...io.WriteCloser) *MultiWriter {
	w := make([]io.WriteCloser, len(writers))
	copy(w, writers)

	return &MultiWriter{writers: w}
}

// WithExtJSON turn JSON output mode on
func (w *MultiWriter) WithExtJSON(containerName, group string) *MultiWriter {
	w.container = containerName
	w.group = group
	w.isJSON = true

	hostname := "unknown"
	if h, err := os.Hostname(); err == nil {
		hostname = h
	}
	w.hostname = hostname
	return w
}

// Write to all writers and ignore errors unless they all have errors
func (w *MultiWriter) Write(p []byte) (n int, err error) {
	pp := p
	if w.isJSON {
		if pp, err = w.extJSON(p); err != nil {
			return 0, errors.Wrap(err, "can't convert message to json")
		}
	}

	numErrors := 0
	for _, w := range w.writers {
		if _, err = w.Write(pp); err != nil {
			numErrors++
		}
	}

	// all writers failed, return error
	if numErrors == len(w.writers) {
		return len(p), errors.Wrap(err, "all writers failed")
	}

	return len(p), nil
}

// Close all writers, collect errors
func (w *MultiWriter) Close() error {
	errs := new(multierror.Error)
	for _, w := range w.writers {
		errs = multierror.Append(w.Close())
	}
	return errs.ErrorOrNil()
}

func (w *MultiWriter) extJSON(p []byte) (res []byte, err error) {
	return json.Marshal(jMsg{Msg: string(p), TS: time.Now(), Host: w.hostname, Group: w.group, Container: w.container})
}
