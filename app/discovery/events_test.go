package discovery

import (
	"log"
	"sync"
	"testing"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvents(t *testing.T) {

	client := &mockDockerClient{}
	events, err := NewEventNotif(client, "tst_exclude")
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	go client.add("id1", "name1")

	ev := <-events.Channel()
	assert.Equal(t, "name1", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")

	go client.remove("id1")
	ev = <-events.Channel()
	assert.Equal(t, "id1", ev.ContainerID)
	assert.Equal(t, false, ev.Status, "stopped")
}

func TestEmit(t *testing.T) {
	client := &mockDockerClient{}
	time.Sleep(10 * time.Millisecond)

	client.add("id1", "name1")
	client.add("id2", "tst_exclude")
	client.add("id2", "name2")

	events, err := NewEventNotif(client, "tst_exclude")
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "name1", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")

	ev = <-events.Channel()
	assert.Equal(t, "name2", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")
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

type mockDockerClient struct {
	containers []dockerclient.APIContainers
	events     chan<- *dockerclient.APIEvents
	sync.Mutex
}

func (m *mockDockerClient) add(id string, name string) {
	m.Lock()
	defer m.Unlock()
	m.containers = append(m.containers, dockerclient.APIContainers{ID: id, Names: []string{name}})
	ev := dockerclient.APIEvents{Type: "container", ID: id, Status: "start"}
	ev.Actor.Attributes = map[string]string{}
	ev.Actor.Attributes["name"] = name
	ev.Actor.ID = id
	if m.events != nil {
		m.events <- &ev
	}
	log.Printf("added %s", id)
}

func (m *mockDockerClient) remove(id string) {
	m.Lock()
	defer m.Unlock()
	r := []dockerclient.APIContainers{}
	for _, c := range m.containers {
		if c.ID != id {
			r = append(r, c)
		}
	}
	m.containers = r
	ev := dockerclient.APIEvents{Type: "container", ID: id, Status: "stop"}
	ev.Actor.ID = id
	if m.events != nil {
		m.events <- &ev
	}
	log.Printf("removed %s", id)

}

func (m *mockDockerClient) ListContainers(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
	m.Lock()
	defer m.Unlock()
	return m.containers, nil
}

func (m *mockDockerClient) AddEventListener(listener chan<- *dockerclient.APIEvents) error {
	m.Lock()
	defer m.Unlock()
	m.events = listener
	return nil
}
