package discovery

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestEvents(t *testing.T) {

	dockerHost := os.Getenv("DOCKER_HOST")
	client, err := dockerclient.NewClient(dockerHost)
	require.NoError(t, err)
	events, err := NewEventNotif(client, "tst_exclude")
	require.NoError(t, err)

	ctx := startTestContainer("tst_lg1")

	for {
		ev := <-events.Channel()
		if ev.ContainerName != "tst_lg1" {
			continue
		}
		assert.Equal(t, "tst_lg1", ev.ContainerName)
		assert.Equal(t, true, ev.Status, "started")
		break
	}
	<-ctx.Done() // terminate container

	for {
		ev := <-events.Channel()
		if ev.ContainerName != "tst_lg1" {
			continue
		}
		assert.Equal(t, "tst_lg1", ev.ContainerName)
		assert.Equal(t, false, ev.Status, "stopped")
		break
	}
}

func TestEmit(t *testing.T) {
	ctx := startTestContainer("tst_lg1")

	dockerHost := os.Getenv("DOCKER_HOST")
	client, err := dockerclient.NewClient(dockerHost)

	require.NoError(t, err)
	events, err := NewEventNotif(client, "tst_exclude")
	require.NoError(t, err)

	for {
		ev := <-events.Channel()
		if ev.ContainerName != "tst_lg1" {
			continue
		}
		assert.Equal(t, "tst_lg1", ev.ContainerName)
		assert.Equal(t, true, ev.Status, "started")
		break
	}

	<-ctx.Done() // terminate container
}

func startTestContainer(name string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "--name="+name, "alpine", "sleep", "3")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}()
	go func() {
		time.Sleep(5 * time.Second)
		cancel()
	}()
	return ctx
}
func TestGroup(t *testing.T) {

	d := EventNotif{}
	tbl := []struct {
		inp string
		out string
	}{
		{
			inp: "docker.umputun.com:5500/radio-t/webstats:latest",
			out: "radio-t",
		},
		{
			inp: "docker.umputun.com/some/webstats",
			out: "some",
		},
		{
			inp: "docker.umputun.com/some/blah/webstats",
			out: "some",
		},
		{
			inp: "docker.umputun.com/webstats:xxx",
			out: "",
		},
	}

	for _, tt := range tbl {
		assert.Equal(t, tt.out, d.group(tt.inp))
	}
}
