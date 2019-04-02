// +build !windows,!nacl,!plan9

package syslog

import (
	"io"
	"log/syslog"
)

func GetWriter(syslogHost, syslogPrefix, containerName string) (io.WriteCloser, error) {
	return syslog.Dial("udp4", syslogHost, syslog.LOG_WARNING|syslog.LOG_DAEMON, syslogPrefix+containerName)
}

func IsSupported() bool {
	return true
}
