package logger

import (
	"context"
	"log"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	docker "github.com/fsouza/go-dockerclient"
)

type mockLogClient struct {
	err error
	ctx context.Context
}

func (m *mockLogClient) Logs(docker.LogsOptions) error {
	select {
	case <-time.After(2 * time.Second):
		log.Print("mock log completed")
		return nil
	case <-m.ctx.Done():
		log.Print("mock log terminated")
		return m.ctx.Err()
	}
}

func TestLogger_WithError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: &mockLogClient{err: nil, ctx: ctx}}
	l = l.Go(ctx)

	go func() {
		// trigger error in mockLogClient
		time.Sleep(1 * time.Second)
		cancel()
	}()

	err := l.DockerClient.Logs(docker.LogsOptions{})
	assert.Error(t, err, "context canceled")
}

func TestLogger_NoError(t *testing.T) {
	ctx := context.Background()
	l := &LogStreamer{ContainerID: "test_id", ContainerName: "test_name", DockerClient: &mockLogClient{err: nil, ctx: ctx}}
	l = l.Go(ctx)

	err := l.DockerClient.Logs(docker.LogsOptions{})
	assert.NoError(t, err)
}
