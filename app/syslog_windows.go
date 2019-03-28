// +build windows nacl plan9

package main

import (
	"errors"
	"io"
)

func getSyslogWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return nil, errors.New("syslog is not supported on this os")
}

func isSyslogSupported() bool {
	return false
}
