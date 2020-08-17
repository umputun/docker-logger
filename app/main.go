package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	docker "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
	"github.com/jessevdk/go-flags"
	"github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/umputun/docker-logger/app/discovery"
	"github.com/umputun/docker-logger/app/logger"
	"github.com/umputun/docker-logger/app/syslog"
)

type cliOpts struct {
	DockerHost string `short:"d" long:"docker" env:"DOCKER_HOST" default:"unix:///var/run/docker.sock" description:"docker host"`

	EnableSyslog bool   `long:"syslog" env:"LOG_SYSLOG" description:"enable logging to syslog"`
	SyslogHost   string `long:"syslog-host" env:"SYSLOG_HOST" default:"127.0.0.1:514" description:"syslog host"`
	SyslogPrefix string `long:"syslog-prefix" env:"SYSLOG_PREFIX" default:"docker/" description:"syslog prefix"`

	EnableFiles   bool   `long:"files" env:"LOG_FILES" description:"enable logging to files"`
	MaxFileSize   int    `long:"max-size" env:"MAX_SIZE" default:"10" description:"size of log triggering rotation (MB)"`
	MaxFilesCount int    `long:"max-files" env:"MAX_FILES" default:"5" description:"number of rotated files to retain"`
	MaxFilesAge   int    `long:"max-age" env:"MAX_AGE" default:"30" description:"maximum number of days to retain"`
	MixErr        bool   `long:"mix-err" env:"MIX_ERR" description:"send error to std output log file"`
	FilesLocation string `long:"loc" env:"LOG_FILES_LOC" default:"logs" description:"log files locations"`

	Excludes        []string `short:"x" long:"exclude" env:"EXCLUDE" env-delim:"," description:"excluded container names"`
	Includes        []string `short:"i" long:"include" env:"INCLUDE" env-delim:"," description:"included container names"`
	IncludesPattern string   `short:"p" long:"include-pattern" env:"INCLUDE_PATTERN" env-delim:"," description:"included container names regex pattern"`
	ExtJSON         bool     `short:"j" long:"json" env:"JSON" description:"wrap message with JSON envelope"`
	Dbg             bool     `long:"dbg" env:"DEBUG" description:"debug mode"`
}

var revision = "unknown"

func main() {
	fmt.Printf("docker-logger %s\n", revision)

	var opts cliOpts
	if _, err := flags.Parse(&opts); err != nil {
		os.Exit(1)
	}
	setupLog(opts.Dbg)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { // catch signal and invoke graceful termination
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		log.Printf("[WARN] interrupt signal")
		cancel()
	}()

	log.Printf("[INFO] options: %+v", opts)
	if err := do(ctx, opts); err != nil {
		log.Printf("[ERROR] failed, %v", err)
	}
}

func do(ctx context.Context, opts cliOpts) error {

	if opts.Includes != nil && opts.IncludesPattern != "" {
		return errors.New("only single option Includes/IncludesPattern are allowed")
	}

	if opts.IncludesPattern != "" {
		_, err := regexp.Compile(opts.IncludesPattern)
		if err != nil {
			return errors.New("could not parse Includes Pattern")
		}
	}

	if opts.Includes != nil && opts.Excludes != nil {
		return errors.New("only single option Excludes/Includes are allowed")
	}

	if opts.EnableSyslog && !syslog.IsSupported() {
		return errors.New("syslog is not supported on this OS")
	}

	client, err := docker.NewClient(opts.DockerHost)
	if err != nil {
		return errors.Wrapf(err, "failed to make docker client %s", err)
	}

	events, err := discovery.NewEventNotif(client, opts.Excludes, opts.Includes, opts.IncludesPattern)
	if err != nil {
		return errors.Wrap(err, "failed to make event notifier")
	}

	runEventLoop(ctx, opts, events, client)
	return nil
}

func runEventLoop(ctx context.Context, opts cliOpts, events *discovery.EventNotif, client *docker.Client) {
	logStreams := map[string]logger.LogStreamer{}

	procEvent := func(event discovery.Event) {

		if event.Status {
			// new/started container detected

			if _, found := logStreams[event.ContainerID]; found {
				log.Printf("[WARN] ignore dbl-start %+v", event)
				return
			}

			logWriter, errWriter := makeLogWriters(opts, event.ContainerName, event.Group)
			ls := logger.LogStreamer{
				DockerClient:  client,
				ContainerID:   event.ContainerID,
				ContainerName: event.ContainerName,
				LogWriter:     logWriter,
				ErrWriter:     errWriter,
			}
			ls = *ls.Go(ctx)
			logStreams[event.ContainerID] = ls
			log.Printf("[DEBUG] streaming for %d containers", len(logStreams))
			return
		}

		// removed/stopped container detected
		ls, ok := logStreams[event.ContainerID]
		if !ok {
			log.Printf("[DEBUG] close loggers event %+v for non-mapped container ignored", event)
			return
		}

		log.Printf("[DEBUG] close loggers for %+v", event)
		ls.Close()

		if e := ls.LogWriter.Close(); e != nil {
			log.Printf("[WARN] failed to close log writer for %+v, %s", event, e)
		}

		if !opts.MixErr { // don't close err writer in mixed mode, closed already by LogWriter.Close()
			if e := ls.ErrWriter.Close(); e != nil {
				log.Printf("[WARN] failed to close err writer for %+v, %s", event, e)
			}
		}
		delete(logStreams, event.ContainerID)
		log.Printf("[DEBUG] streaming for %d containers", len(logStreams))
	}

	for {
		select {
		case <-ctx.Done():
			log.Print("[WARN] event loop terminated")
			for _, v := range logStreams {
				v.Close()
				log.Printf("[INFO] close logger stream for %s", v.ContainerName)
			}
			return
		case event := <-events.Channel():
			log.Printf("[DEBUG] received event %+v", event)
			procEvent(event)
		}
	}

}

// makeLogWriters creates io.Writer with rotated out and separate err files. Also adds writer for remote syslog
func makeLogWriters(opts cliOpts, containerName string, group string) (logWriter, errWriter io.WriteCloser) {
	log.Printf("[DEBUG] create log writer for %s", strings.TrimPrefix(group+"/"+containerName, "/"))
	if !opts.EnableFiles && !opts.EnableSyslog {
		log.Fatalf("[ERROR] either files or syslog has to be enabled")
	}

	var logWriters []io.WriteCloser // collect log writers here, for MultiWriter use
	var errWriters []io.WriteCloser // collect err writers here, for MultiWriter use

	if opts.EnableFiles {

		logDir := opts.FilesLocation
		if group != "" {
			logDir = fmt.Sprintf("%s/%s", opts.FilesLocation, group)
		}
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Fatalf("[ERROR] can't make directory %s, %v", logDir, err)
		}

		logName := fmt.Sprintf("%s/%s.log", logDir, containerName)
		logFileWriter := &lumberjack.Logger{
			Filename:   logName,
			MaxSize:    opts.MaxFileSize, // megabytes
			MaxBackups: opts.MaxFilesCount,
			MaxAge:     opts.MaxFilesAge, // in days
			Compress:   true,
		}

		// use std writer for errors by default
		errFileWriter := logFileWriter
		errFname := logName

		if !opts.MixErr { // if writers not mixed make error writer
			errFname = fmt.Sprintf("%s/%s.err", logDir, containerName)
			errFileWriter = &lumberjack.Logger{
				Filename:   errFname,
				MaxSize:    opts.MaxFileSize, // megabytes
				MaxBackups: opts.MaxFilesCount,
				MaxAge:     opts.MaxFilesAge, // in days
				Compress:   true,
			}
		}

		logWriters = append(logWriters, logFileWriter)
		errWriters = append(errWriters, errFileWriter)
		log.Printf("[INFO] loggers created for %s and %s, max.size=%dM, max.files=%d, max.days=%d",
			logName, errFname, opts.MaxFileSize, opts.MaxFilesCount, opts.MaxFilesAge)
	}

	if opts.EnableSyslog && syslog.IsSupported() {
		syslogWriter, err := syslog.GetWriter(opts.SyslogHost, opts.SyslogPrefix, containerName)

		if err == nil {
			logWriters = append(logWriters, syslogWriter)
			errWriters = append(errWriters, syslogWriter)
		} else {
			log.Printf("[WARN] can't connect to syslog, %v", err)
		}
	}

	lw := logger.NewMultiWriterIgnoreErrors(logWriters...)
	ew := logger.NewMultiWriterIgnoreErrors(errWriters...)
	if opts.ExtJSON {
		lw = lw.WithExtJSON(containerName, group)
		ew = ew.WithExtJSON(containerName, group)
	}

	return lw, ew
}

func setupLog(dbg bool) {
	if dbg {
		log.Setup(log.Debug, log.CallerFile, log.CallerFunc, log.Msec, log.LevelBraces)
		return
	}
	log.Setup(log.Msec, log.LevelBraces, log.CallerPkg)
}
