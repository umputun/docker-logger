package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"os"
	"os/signal"
	"syscall"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/hashicorp/logutils"
	"github.com/jessevdk/go-flags"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/umputun/docker-logger/app/discovery"
	"github.com/umputun/docker-logger/app/logger"
)

var opts struct {
	DockerHost    string   `short:"d" long:"docker" env:"DOCKER_HOST" default:"unix:///var/run/docker.sock" description:"docker host"`
	SyslogHost    string   `long:"syslog-host" env:"SYSLOG_HOST" default:"127.0.0.1:514" description:"syslog host"`
	EnableFiles   bool     `long:"files" env:"LOG_FILES" description:"enable logging to files"`
	EnableSyslog  bool     `long:"syslog" env:"LOG_SYSLOG" description:"enable logging to syslog"`
	MaxFileSize   int      `long:"max-size" env:"MAX_SIZE" default:"10" description:"size of log triggering rotation (MB)"`
	MaxFilesCount int      `long:"max-files" env:"MAX_FILES" default:"5" description:"number of rotated files to retain"`
	MaxFilesAge   int      `long:"max-age" env:"MAX_AGE" default:"30" description:"maximum number of days to retain"`
	Excludes      []string `short:"x" long:"exclude" env:"EXCLUDE" env-delim:"," description:"excluded container names"`
	ExtJSON       bool     `short:"j" long:"json" env:"JSON" description:"wrap message with JSON envelope"`
	Dbg           bool     `long:"dbg" env:"DEBUG" description:"debug mode"`
}

var revision = "unknown"

func main() {
	fmt.Printf("docker-logger %s\n", revision)
	if _, err := flags.Parse(&opts); err != nil {
		os.Exit(1)
	}
	setupLog(opts.Dbg)

	log.Printf("[INFO] options: %+v", opts)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { // catch signal and invoke graceful termination
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		log.Print("[WARN] interrupt signal")
		cancel()
	}()

	client, err := dockerclient.NewClient(opts.DockerHost)
	if err != nil {
		log.Fatalf("[ERROR] failed to make docker client %s, %v", opts.DockerHost, err)
	}

	events, err := discovery.NewEventNotif(client, opts.Excludes)
	if err != nil {
		log.Fatalf("[ERROR] failed to make event notifier, %v", err)
	}

	runEventLoop(ctx, events, client)
}

func runEventLoop(ctx context.Context, events *discovery.EventNotif, client *dockerclient.Client) {
	containerLogs := map[string]logger.LogStreamer{}

	procEvent := func(event discovery.Event) {

		if event.Status {
			// new/started container detected
			logWriter, errWriter := makeLogWriters(event.ContainerName, event.Group, opts.ExtJSON)
			ctx, cancel := context.WithCancel(context.Background())
			ls := logger.LogStreamer{
				Context:       ctx,
				CancelFn:      cancel,
				DockerClient:  client,
				ContainerID:   event.ContainerID,
				ContainerName: event.ContainerName,
				LogWriter:     logWriter,
				ErrWriter:     errWriter,
			}
			containerLogs[event.ContainerID] = ls
			ls.Go()
			return
		}

		// removed/stopped container detected
		ls, ok := containerLogs[event.ContainerID]
		if !ok {
			log.Printf("[DEBUG] close loggers event %+v for non-mapped container", event)
			return
		}

		log.Printf("[DEBUG] close loggers for %+v", event)
		ls.CancelFn()
		if e := ls.LogWriter.Close(); e != nil {
			log.Printf("[WARN] failed to close log writer for %+v, %s", event, e)
		}
		if e := ls.ErrWriter.Close(); e != nil {
			log.Printf("[WARN] failed to close err writer for %+v, %s", event, e)
		}
		delete(containerLogs, event.ContainerID)
	}

	for {
		select {
		case <-ctx.Done():
			log.Print("[WARN] event loop terminated")
			return
		case event := <-events.Channel():
			procEvent(event)
		}
	}

}

// makeLogWriters creates io.Writer with rotated out and separate err files. Also adds writer for remote syslog
func makeLogWriters(containerName string, group string, isExt bool) (logWriter, errWriter io.WriteCloser) {
	log.Printf("[DEBUG] create log writer for %s/%s", group, containerName)
	if !opts.EnableFiles && !opts.EnableSyslog {
		log.Printf("[ERROR] either files or syslog has to be enabled")
	}

	logWriters := []io.WriteCloser{} // collect log writers here, for MultiWriter use
	errWriters := []io.WriteCloser{} // collect err writers here, for MultiWriter use

	if opts.EnableFiles {

		logDir := "logs"
		if group != "" {
			logDir = fmt.Sprintf("logs/%s", group)
		}
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Fatalf("[ERROR] can't make directory %s, %v", logDir, err)
		}

		logName := fmt.Sprintf("%s/%s.log", logDir, containerName)
		logFileWriter := lumberjack.Logger{
			Filename:   logName,
			MaxSize:    opts.MaxFileSize, // megabytes
			MaxBackups: opts.MaxFilesCount,
			MaxAge:     opts.MaxFilesAge, //days
			Compress:   true,
		}

		errFname := fmt.Sprintf("%s/%s.err", logDir, containerName)
		errFileWriter := lumberjack.Logger{
			Filename:   errFname,
			MaxSize:    opts.MaxFileSize, // megabytes
			MaxBackups: opts.MaxFilesCount,
			MaxAge:     opts.MaxFilesAge, //days
			Compress:   true,
		}

		logWriters = append(logWriters, &logFileWriter)
		errWriters = append(errWriters, &errFileWriter)
		log.Printf("[INFO] loggers created for %s and %s, max.size=%dM, max.files=%d, max.days=%d",
			logName, errFname, opts.MaxFileSize, opts.MaxFilesCount, opts.MaxFilesAge)
	}

	if opts.EnableSyslog {
		syslogWriter, err := syslog.Dial("udp4", opts.SyslogHost, syslog.LOG_WARNING|syslog.LOG_DAEMON, "docker/"+containerName)

		if err == nil {
			logWriters = append(logWriters, syslogWriter)
			errWriters = append(errWriters, syslogWriter)
		} else {
			log.Printf("[WARN] can't connect to syslog, %v", err)
		}
	}

	lw := logger.NewMultiWriterIgnoreErrors(logWriters...)
	ew := logger.NewMultiWriterIgnoreErrors(errWriters...)
	if isExt {
		lw = lw.WithExtJSON(containerName, group)
		ew = ew.WithExtJSON(containerName, group)
	}

	return lw, ew
}

func setupLog(dbg bool) {
	filter := &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"DEBUG", "INFO", "WARN", "ERROR"},
		MinLevel: logutils.LogLevel("INFO"),
		Writer:   os.Stdout,
	}

	log.SetFlags(log.Ldate | log.Ltime)

	if dbg {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
		filter.MinLevel = logutils.LogLevel("DEBUG")
	}
	log.SetOutput(filter)
}
