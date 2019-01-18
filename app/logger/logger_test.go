package logger

import (
	"context"
	"testing"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
	"github.com/stretchr/testify/assert"
)

type mockLogClient struct {
	err error
	ctx context.Context
}

func (m *mockLogClient) Logs(opts docker.LogsOptions) error {
	select {
	case <-time.After(2 * time.Second):
		log.Printf("mock log completed %+v", opts)
		m.err = nil
	case <-m.ctx.Done():
		log.Printf("mock log terminated %+v", opts)
		m.err = m.ctx.Err()
	}
	return m.err
}

func TestLogger_WithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mock := mockLogClient{err: nil, ctx: ctx}
	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: &mock}
	l = l.Go(ctx)
	st := time.Now()
	go func() {
		// trigger error in mockLogClient
		time.Sleep(1 * time.Second)
		cancel()
	}()
	l.Wait()
	assert.True(t, time.Since(st) < time.Second*2, "terminated early")
}

func TestLogger_Cancel(t *testing.T) {
	ctx := context.Background()
	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: &mockLogClient{err: nil, ctx: ctx}}
	l = l.Go(ctx)

	go func() {
		// trigger close
		time.Sleep(2 * time.Second)
		l.Close()
	}()
	st := time.Now()
	l.Wait()
	assert.True(t, time.Since(st) >= time.Second*2, "completed")
}
