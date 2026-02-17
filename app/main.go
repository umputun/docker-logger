package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
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
	ExcludesPattern string   `short:"e" long:"exclude-pattern" env:"EXCLUDE_PATTERN" env-delim:"," description:"excluded container names regex pattern"`
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
	if err := do(ctx, &opts); err != nil {
		log.Printf("[ERROR] failed, %v", err)
		os.Exit(1)
	}
}

func do(ctx context.Context, opts *cliOpts) error {
	if opts.Includes != nil && opts.IncludesPattern != "" {
		return errors.New("only single option Includes/IncludesPattern are allowed")
	}

	if opts.Includes != nil && opts.Excludes != nil {
		return errors.New("only single option Excludes/Includes are allowed")
	}

	if opts.Excludes != nil && opts.ExcludesPattern != "" {
		return errors.New("only single option Excludes/ExcludesPattern are allowed")
	}

	if opts.IncludesPattern != "" && opts.ExcludesPattern != "" {
		return errors.New("only single option IncludesPattern/ExcludesPattern are allowed")
	}

	if opts.Includes != nil && opts.ExcludesPattern != "" {
		return errors.New("only single option Includes/ExcludesPattern are allowed")
	}

	if opts.IncludesPattern != "" && opts.Excludes != nil {
		return errors.New("only single option IncludesPattern/Excludes are allowed")
	}

	if !opts.EnableFiles && !opts.EnableSyslog {
		return errors.New("at least one log destination must be enabled (files or syslog)")
	}

	if opts.EnableSyslog && !syslog.IsSupported() {
		return errors.New("syslog is not supported on this OS")
	}

	client, err := docker.NewClient(opts.DockerHost)
	if err != nil {
		return errors.Wrap(err, "failed to make docker client")
	}

	events, err := discovery.NewEventNotif(client, discovery.EventNotifOpts{
		Excludes:        opts.Excludes,
		Includes:        opts.Includes,
		IncludesPattern: opts.IncludesPattern,
		ExcludesPattern: opts.ExcludesPattern,
	})
	if err != nil {
		return errors.Wrap(err, "failed to make event notifier")
	}

	return runEventLoop(ctx, opts, events.Channel(), events.Err(), client)
}

func runEventLoop(ctx context.Context, opts *cliOpts, eventsCh <-chan discovery.Event,
	listenerErr <-chan error, logClient logger.LogClient) error {
	logStreams := map[string]*logger.LogStreamer{}

	procEvent := func(event discovery.Event) {
		if event.Status {
			// new/started container detected

			if _, found := logStreams[event.ContainerID]; found {
				log.Printf("[WARN] ignore dbl-start %+v", event)
				return
			}

			logWriter, errWriter, err := makeLogWriters(opts, event.ContainerName, event.Group)
			if err != nil {
				log.Printf("[WARN] failed to create log writers for %s, %v", event.ContainerName, err)
				return
			}
			ls := &logger.LogStreamer{
				DockerClient:  logClient,
				ContainerID:   event.ContainerID,
				ContainerName: event.ContainerName,
				LogWriter:     logWriter,
				ErrWriter:     errWriter,
			}
			ls.Go(ctx)
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
		closeStreamer(ls, event.ContainerName, opts.MixErr)
		delete(logStreams, event.ContainerID)
		log.Printf("[DEBUG] streaming for %d containers", len(logStreams))
	}

	closeAll := func() {
		for _, v := range logStreams {
			closeStreamer(v, v.ContainerName, opts.MixErr)
			log.Printf("[INFO] close logger stream for %s", v.ContainerName)
		}
	}

	for {
		select {
		case <-ctx.Done():
			log.Print("[WARN] event loop terminated")
			closeAll()
			return nil
		case err := <-listenerErr:
			log.Printf("[ERROR] event listener failed, %v", err)
			closeAll()
			return err
		case event, ok := <-eventsCh:
			if !ok {
				log.Print("[WARN] events channel closed, terminating event loop")
				closeAll()
				// check if the listener sent a more specific error before closing the channel
				select {
				case err := <-listenerErr:
					return err
				default:
					return errors.New("events channel closed unexpectedly")
				}
			}
			log.Printf("[DEBUG] received event %+v", event)
			procEvent(event)
		}
	}
}

func closeStreamer(ls *logger.LogStreamer, name string, mixErr bool) {
	ls.Close()
	if e := ls.LogWriter.Close(); e != nil {
		log.Printf("[WARN] failed to close log writer for %s, %s", name, e)
	}
	if !mixErr {
		if e := ls.ErrWriter.Close(); e != nil {
			log.Printf("[WARN] failed to close err writer for %s, %s", name, e)
		}
	}
}

// makeLogWriters creates io.WriteCloser with rotated out and separate err files. Also adds writer for remote syslog
func makeLogWriters(opts *cliOpts, containerName, group string) (logWriter, errWriter io.WriteCloser, err error) {
	log.Printf("[DEBUG] create log writer for %s", strings.TrimPrefix(group+"/"+containerName, "/"))
	if !opts.EnableFiles && !opts.EnableSyslog {
		return nil, nil, errors.New("either files or syslog has to be enabled")
	}

	var logWriters []io.WriteCloser // collect log writers here, for MultiWriter use
	var errWriters []io.WriteCloser // collect err writers here, for MultiWriter use

	if opts.EnableFiles {
		logDir := opts.FilesLocation
		if group != "" {
			logDir = fmt.Sprintf("%s/%s", opts.FilesLocation, group)
		}
		if err := os.MkdirAll(logDir, 0o750); err != nil {
			return nil, nil, errors.Wrapf(err, "can't make directory %s", logDir)
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
			errWriters = append(errWriters, writeNopCloser{syslogWriter}) // wrap to prevent double-close
		} else {
			log.Printf("[ERROR] can't connect to syslog, %v", err)
		}
	}

	if len(logWriters) == 0 {
		return nil, nil, errors.New("no log destinations available")
	}

	lw := logger.NewMultiWriterIgnoreErrors(logWriters...)
	ew := logger.NewMultiWriterIgnoreErrors(errWriters...)
	if opts.ExtJSON {
		lw = lw.WithExtJSON(containerName, group)
		ew = ew.WithExtJSON(containerName, group)
	}

	return lw, ew, nil
}

// writeNopCloser wraps an io.Writer with a no-op Close method.
// used to prevent double-close when the same writer (e.g., syslog) is shared between log and err MultiWriters.
type writeNopCloser struct {
	io.Writer
}

func (writeNopCloser) Close() error { return nil }

func setupLog(dbg bool) {
	if dbg {
		log.Setup(log.Debug, log.CallerFile, log.CallerFunc, log.Msec, log.LevelBraces)
		return
	}
	log.Setup(log.Msec, log.LevelBraces, log.CallerPkg)
}
