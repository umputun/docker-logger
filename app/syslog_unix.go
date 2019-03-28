// +build !windows,!nacl,!plan9

package main

import (
	"io"
	"log/syslog"
)

func getSyslogWriter(syslogHost, containerName string) (io.WriteCloser, error) {
	return syslog.Dial("udp4", syslogHost, syslog.LOG_WARNING|syslog.LOG_DAEMON, "docker/"+containerName)
}

func isSyslogSupported() bool {
	return true
}
