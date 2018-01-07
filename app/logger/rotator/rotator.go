// Package rotator provides Writer with rolling and compressed history.
// Writer works on top of buffered bufio.Writer flushing on 500ms of inactivity.
// Optionally bufferSize can be set to enforce flush every N line. Rotation happens on size
// and can be turned on day change (midnight).
package rotator

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// activeFileWriter implements io.Writer, flushes in background. Thread safe
type activeFileWriter struct {
	path             string
	maxFiles         int
	maxSize          int64
	bufferSize       int
	flushDuration    time.Duration
	midnightRotation bool

	ch chan []byte
}

// New creates buffered writer with auto-flush
func New(path string, options ...Option) (io.WriteCloser, error) {
	log.Printf("[DEBUG] new writer for %s", path)

	res := activeFileWriter{
		ch:            make(chan []byte, 1000),
		path:          path,
		maxFiles:      10,
		maxSize:       10000000, // size in bytes
		bufferSize:    1,        // buffered lines,
		flushDuration: time.Millisecond * 500,
	}

	for _, opt := range options {
		if err := opt(&res); err != nil {
			log.Printf("[WARN] failed to set logrot option, %v", err)
		}
	}

	go res.do()
	return &res, nil
}

// Write to channel
func (aw *activeFileWriter) Write(p []byte) (nn int, err error) {
	w := make([]byte, len(p))
	copy(w, p)

	aw.ch <- w
	return len(w), nil
}

// Close writer, kill goroutine
func (aw *activeFileWriter) Close() error {
	close(aw.ch)
	return nil
}

// do listens on input channel and send []byte to buffered writer. On timeout flushes if has something.
// every 1sec checks for day change to perform daily rotation.
func (aw *activeFileWriter) do() {
	log.Printf("[DEBUG] activate writer loop for %s", aw.path)
	defer log.Printf("[DEBUG] background proc closed for %s", aw.path)

	bufferedLines := 0
	currentDay := time.Now()
	ticker := time.NewTicker(time.Second)

	fh, err := os.OpenFile(aw.path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		log.Printf("[WARN] failed to open %s, %s", aw.path, err)
		return
	}
	writer := bufio.NewWriter(fh)
	lastFlush := time.Now()

	for {
		select {

		case msg, more := <-aw.ch: // primary path, gets new message

			// terminate on close
			if !more {
				if err = writer.Flush(); err != nil {
					log.Printf("[WARN] failed to flush %s, %v", aw.path, err)
				}
				return
			}

			if len(msg) == 0 {
				continue
			}

			// append \n to line
			if msg[len(msg)-1] != byte('\n') {
				msg = append(msg, byte('\n'))
			}

			_, err = writer.Write(msg)
			if err != nil {
				log.Printf("[WARN] failed to write %s - %s, %v", aw.path, string(msg), err)
				continue
			}
			// flush on bufferSize
			bufferedLines++
			if bufferedLines >= aw.bufferSize {
				if err = writer.Flush(); err != nil {
					log.Printf("[WARN] failed to flush %s, %v", aw.path, err)
					continue
				}
				bufferedLines = 0
				lastFlush = time.Now()
			}

		case <-ticker.C: // ticks every second

			// no activity since lastFlush and something in buffer
			if bufferedLines > 0 && time.Since(lastFlush) > aw.flushDuration {
				if err = writer.Flush(); err != nil {
					log.Printf("[WARN] failed to flush %s, %v", aw.path, err)
					continue
				}
				bufferedLines = 0
			}

			// rotation check for max file size
			if fi, stErr := os.Stat(aw.path); stErr == nil {
				if aw.maxSize > 0 && fi.Size() >= aw.maxSize {
					log.Printf("[DEBUG] file %s reached max size %d of %d, rotation triggered", aw.path, fi.Size(), aw.maxSize)
					if err = writer.Flush(); err != nil {
						log.Printf("[WARN] failed to flush %s, %v", aw.path, err)
					}
					fh, writer, err = aw.rotate(fh)
					if err != nil {
						log.Printf("[ERROR] failed to rotate, %v", err)
					}
				}
			}

			// midnight rotation
			if aw.midnightRotation && (time.Now().YearDay() != currentDay.YearDay()) {
				currentDay = time.Now()
				bufferedLines = 0
				if err = writer.Flush(); err != nil {
					log.Printf("[WARN] failed to flush %s, %v", aw.path, err)
				}
				fh, writer, err = aw.rotate(fh)
				if err != nil {
					log.Printf("[ERROR] failed to rotate, %v", err)
				}
			}
		}
	}
}

func (aw *activeFileWriter) rotate(fh *os.File) (*os.File, *bufio.Writer, error) {
	log.Printf("[DEBUG] rotate %s", fh.Name())

	// close original file
	if err := fh.Close(); err != nil {
		return nil, nil, err
	}

	// find highest n such that <path>.<n>.gz exists
	highNum := func() (result int, err error) {
		for n := 1; n <= aw.maxFiles; n++ {
			_, err = os.Lstat(fmt.Sprintf("%s.%d.gz", fh.Name(), n))
			if err != nil && !os.IsNotExist(err) {
				return 0, err
			}
			if err != nil {
				break
			}
			result = n
		}
		return result, nil
	}

	n, highNumErr := highNum()
	if highNumErr != nil {
		return nil, nil, highNumErr
	}

	// delete expired gz files
	for ; n > aw.maxFiles-2 && n > 0; n-- {
		e := os.Remove(fmt.Sprintf("%s.%d.gz", fh.Name(), n))
		if e != nil && !os.IsNotExist(e) {
			return nil, nil, e
		}
	}

	// move each gz file up one number
	for ; n > 0; n-- {
		err := os.Rename(fmt.Sprintf("%s.%d.gz", fh.Name(), n), fmt.Sprintf("%s.%d.gz", fh.Name(), n+1))
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
	}

	// copy file contents to <path>.1.gz
	if err := aw.compress(fh.Name(), fmt.Sprintf("%s.1.gz", fh.Name())); err != nil {
		return nil, nil, err
	}

	// reopen and truncate the original
	fh, err := os.OpenFile(aw.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Printf("[WARN] failed to open %s, %s", aw.path, err)
		return nil, nil, err
	}

	writer := bufio.NewWriter(fh)

	log.Printf("[DEBUG] rotation for %s completed", fh.Name())
	return fh, writer, nil
}

func (aw *activeFileWriter) compress(srcFile string, destGz string) error {

	log.Printf("[DEBUG] gzip %s to %s", srcFile, destGz)

	w, err := os.OpenFile(destGz, os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}

	gw := gzip.NewWriter(w)

	file, err := os.OpenFile(srcFile, os.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(gw, file); err != nil {
		return err
	}

	if err = file.Close(); err != nil {
		return err
	}
	if err = gw.Close(); err != nil {
		return err
	}
	return nil
}
