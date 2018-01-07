package rotator

import (
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRotation(t *testing.T) {
	location := "/tmp/logs-test.rotated"
	cleanLocation(t, location)

	err := os.MkdirAll(location, 0777)
	assert.Nil(t, err)

	wr, err := New(location+"/test.log", MaxSize(201), Buffer(1))
	assert.Nil(t, err)
	assert.NotNil(t, wr)

	for i := 0; i < 10; i++ {
		_, e := wr.Write([]byte("1234567890123456789")) // 20 bytes
		assert.Nil(t, e)
	}
	time.Sleep(1200 * time.Millisecond)

	fi, err := os.Lstat(location + "/test.log")
	assert.Nil(t, err)
	assert.Equal(t, int64(200), fi.Size(), "write all 200, buffered")

	_, e := wr.Write([]byte("1234567890123456789\n")) // 20 bytes
	assert.Nil(t, e)
	time.Sleep(1200 * time.Millisecond)

	_, err = os.Lstat(location + "/test.log.1.gz")
	assert.Nil(t, err, "test.1.gz should be created")

	// trigger another rotation
	for i := 0; i < 11; i++ {
		_, e = wr.Write([]byte("1234567890123456789")) // 20 bytes
		assert.Nil(t, e)
	}
	time.Sleep(1200 * time.Millisecond)
	_, err = os.Lstat(location + "/test.log.2.gz")
	assert.Nil(t, err, "test.2.gz should be created")

	cleanLocation(t, location)
}

func TestAppend(t *testing.T) {
	location := "/tmp/logs-test.rotated"
	cleanLocation(t, location)

	err := os.MkdirAll(location, 0777)
	assert.Nil(t, err)

	data := ""
	for n := 0; n < 100; n++ {
		data = data + "987654321\n"
	}
	err = ioutil.WriteFile(location+"/test-append.log", []byte(data), 0600)
	assert.Nil(t, err)

	wr, err := New(location+"/test-append.log", MaxSize(1000000), Buffer(1))
	assert.Nil(t, err)
	assert.NotNil(t, wr)

	for i := 0; i < 10; i++ {
		_, e := wr.Write([]byte("1234567890123456789\n")) // 20 bytes
		assert.Nil(t, e)
	}
	time.Sleep(1200 * time.Millisecond)

	fi, err := os.Lstat(location + "/test-append.log")
	assert.Nil(t, err)
	assert.Equal(t, int64(200+1000), fi.Size(), "write all 1200, buffered")

}

func TestClose(t *testing.T) {
	location := "/tmp/logs-test.rotated"
	cleanLocation(t, location)

	err := os.MkdirAll(location, 0777)
	assert.Nil(t, err)

	data := ""
	for n := 0; n < 100; n++ {
		data = data + "987654321\n"
	}
	err = ioutil.WriteFile(location+"/test-append.log", []byte(data), 0600)
	assert.Nil(t, err)

	wr, err := New(location+"/test-append.log", MaxSize(1000000), Buffer(55))
	assert.Nil(t, err)
	assert.NotNil(t, wr)

	for i := 0; i < 10; i++ {
		_, e := wr.Write([]byte("1234567890123456789\n")) // 20 bytes
		assert.Nil(t, e)
	}
	wr.Close()
	time.Sleep(100 * time.Millisecond)

	fi, err := os.Lstat(location + "/test-append.log")
	assert.Nil(t, err)
	assert.Equal(t, int64(200+1000), fi.Size(), "write all 1200, buffered")

}

func TestSelf(t *testing.T) {
	data, err := ioutil.ReadFile("writer_test.go")
	assert.Nil(t, err)

	location := "/tmp/logs-test.rotated"
	cleanLocation(t, location)
	err = os.MkdirAll(location, 0777)
	assert.Nil(t, err)

	wr, err := New(location+"/test-self.log", MaxSize(1000000), Buffer(1))
	assert.Nil(t, err)

	for _, line := range strings.Split(string(data), "\n") {
		_, e := wr.Write([]byte(line + "\n"))
		assert.Nil(t, e)
	}
	time.Sleep(1200 * time.Millisecond)
	_, err = os.Lstat(location + "/test-self.log")
	assert.Nil(t, err)
	rdata, err := ioutil.ReadFile(location + "/test-self.log")
	assert.Nil(t, err)
	assert.Equal(t, rdata[:len(rdata)-1], data)
}

func cleanLocation(t *testing.T, location string) {
	if _, err := os.Lstat(location); err == nil {
		e := filepath.Walk(location, func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				_ = os.Remove(path)
				log.Printf("remove %s", path)
			}
			return nil
		})
		assert.Nil(t, e)
		log.Printf("remove %s", location)
		_ = os.Remove(location)
	}
}
