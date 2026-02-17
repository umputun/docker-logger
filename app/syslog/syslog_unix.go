//go:build !windows && !nacl && !plan9

package syslog

import (
	"io"
	"log/syslog"
)

// GetWriter returns syslog writer for given host, prefix and container name.
// The syslogPrefix is prepended to containerName to form the syslog tag.
func GetWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return syslog.Dial("udp4", syslogHost, syslog.LOG_WARNING|syslog.LOG_DAEMON, syslogPrefix+containerName)
}

// IsSupported returns true if syslog is supported on this platform
func IsSupported() bool {
	return true
}
