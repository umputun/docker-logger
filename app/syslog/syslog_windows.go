//go:build windows || nacl || plan9
// +build windows nacl plan9

package syslog

import (
	"errors"
	"io"
)

// GetWriter returns an error on unsupported platforms (windows, nacl, plan9)
func GetWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return nil, errors.New("syslog is not supported on this os")
}

// IsSupported returns false on unsupported platforms (windows, nacl, plan9)
func IsSupported() bool {
	return false
}
