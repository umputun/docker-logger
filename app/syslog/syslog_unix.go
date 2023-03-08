//go:build !windows && !nacl && !plan9

package syslog

import (
	"io"
	"log/syslog"
)

// GetWriter returns syslog writer for given host and container
func GetWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return syslog.Dial("udp4", syslogHost, syslog.LOG_WARNING|syslog.LOG_DAEMON, syslogPrefix+containerName)
}

// IsSupported returns true if syslog is supported on this platform
func IsSupported() bool {
	return true
}
