// +build windows nacl plan9

package syslog

import (
	"errors"
	"io"
)

func GetWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return nil, errors.New("syslog is not supported on this os")
}

func IsSupported() bool {
	return false
}
