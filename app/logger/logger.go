package logger

import (
	"io"
	"log"
	"time"

	"github.com/fsouza/go-dockerclient"
)

// LogStreamer connects and activates container's log stream with io.Writer
type LogStreamer struct {
	DockerClient  *docker.Client
	ContainerID   string
	ContainerName string
	LogWriter     io.WriteCloser
	ErrWriter     io.WriteCloser
}

// Go activates streamer
func (l *LogStreamer) Go() {
	log.Printf("[INFO] start log streamer for %s", l.ContainerName)
	go func() {
		logOpts := docker.LogsOptions{
			Container:         l.ContainerID,
			OutputStream:      l.LogWriter, // logs writer for stdout
			ErrorStream:       l.ErrWriter, // err writer for stderr
			Tail:              "10",
			Follow:            true,
			Stdout:            true,
			Stderr:            true,
			InactivityTimeout: time.Hour * 10000,
		}
		err := l.DockerClient.Logs(logOpts) // this is blocking call. Will run until container up and will publish to streams
		log.Printf("[INFO] stream from %s terminated, %v", l.ContainerID, err)
	}()
}
