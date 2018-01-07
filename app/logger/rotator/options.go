package rotator

import "time"

// Option func type
type Option func(aw *activeFileWriter) error

// MaxSize sets size of file triggering rotation. 0 value disable size-based rotation
func MaxSize(size int64) Option {
	return func(aw *activeFileWriter) error {
		aw.maxSize = size
		return nil
	}
}

// MaxFiles sets number of rotated gz files
func MaxFiles(num int) Option {
	return func(aw *activeFileWriter) error {
		aw.maxFiles = num
		return nil
	}
}

// Buffer sets number of lines buffered before the actual write to file.
func Buffer(num int) Option {
	return func(aw *activeFileWriter) error {
		aw.bufferSize = num
		return nil
	}
}

// Midnight turn night rotation on/off.
func Midnight(val bool) Option {
	return func(aw *activeFileWriter) error {
		aw.midnightRotation = val
		return nil
	}
}

// Interval sets max interval before the actual write to file.
func Interval(duration time.Duration) Option {
	return func(aw *activeFileWriter) error {
		aw.flushDuration = duration
		return nil
	}
}
