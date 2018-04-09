package logger

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/hashicorp/go-multierror"
)

// MultiWriter implements WriteCloser for multiple destinations.
// It is simplified version of stdlib MultiWriter. Ignores write error and don't stop the loop.
type MultiWriter struct {
	writers   []io.WriteCloser
	hostname  string
	container string
	group     string
	isExt     bool
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

func (w *MultiWriter) WithExtJSON(containerName string, group string) *MultiWriter {
	w.container = containerName
	w.group = group
	w.isExt = true

	hname := "unknown"
	if h, err := os.Hostname(); err == nil {
		hname = h
	}
	w.hostname = hname
	return w
}

// Write to all writers and ignore errors unless they all have errors
func (w *MultiWriter) Write(p []byte) (n int, err error) {
	pp := p
	if w.isExt {
		if pp, err = w.extJSON(p); err != nil {
			return 0, err
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
		return len(p), err
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
