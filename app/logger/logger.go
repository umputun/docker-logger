package logger

import (
	"context"
	"io"
	"log"
	"strings"
	"time"

	"github.com/fsouza/go-dockerclient"
)

// LogStreamer connects and activates container's log stream with io.Writer
type LogStreamer struct {
	DockerClient  *docker.Client
	ContainerID   string
	ContainerName string

	LogWriter io.WriteCloser
	ErrWriter io.WriteCloser

	Context  context.Context
	CancelFn context.CancelFunc
}

// Go activates streamer
func (l *LogStreamer) Go() {
	log.Printf("[INFO] start log streamer for %s", l.ContainerName)
	go func() {
		logOpts := docker.LogsOptions{
			Context:           l.Context,
			Container:         l.ContainerID,
			OutputStream:      l.LogWriter, // logs writer for stdout
			ErrorStream:       l.ErrWriter, // err writer for stderr
			Tail:              "10",
			Follow:            true,
			Stdout:            true,
			Stderr:            true,
			InactivityTimeout: time.Hour * 10000,
		}

		var err error
		for {
			err = l.DockerClient.Logs(logOpts) // this is blocking call. Will run until container up and will publish to streams
			// workaround https://github.com/moby/moby/issues/35370 with empty log, try read log as empty
			if err != nil && strings.HasPrefix(err.Error(), "error from daemon in stream: Error grabbing logs: EOF") {
				logOpts.Tail = ""
				time.Sleep(1 * time.Second) // prevent busy loop
				continue
			}
			break
		}

		log.Printf("[WARN] stream from %s terminated, %v", l.ContainerID, err)
	}()
}
