package logger

import (
	"io"

	multierror "github.com/hashicorp/go-multierror"
)

// MultiWriter implements WriteCloser for multiple destinations.
// It is simplified version of stdlib MultiWriter. Ignores write error and don't stop the loop.
type MultiWriter struct {
	writers []io.WriteCloser
}

// NewMultiWriterIgnoreErrors create WriteCloser for multiple destinations
func NewMultiWriterIgnoreErrors(writers ...io.WriteCloser) io.WriteCloser {
	w := make([]io.WriteCloser, len(writers))
	copy(w, writers)
	return &MultiWriter{w}
}

// Write to all writers and ignore errors unless they all have errors
func (w *MultiWriter) Write(p []byte) (n int, err error) {
	numErrors := 0
	for _, w := range w.writers {
		if _, err = w.Write(p); err != nil {
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
