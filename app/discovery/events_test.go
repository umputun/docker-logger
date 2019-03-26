package discovery

import (
	"sync"
	"testing"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvents(t *testing.T) {

	client := &mockDockerClient{}
	events, err := NewEventNotif(client, []string{"tst_exclude"}, []string{})
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

func TestEventsIncludes(t *testing.T) {
	client := &mockDockerClient{}
	events, err := NewEventNotif(client, []string{}, []string{"tst_included"})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	go client.add("id2", "tst_included")

	ev := <-events.Channel()
	assert.Equal(t, "tst_included", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")

	go client.remove("id2")
	ev = <-events.Channel()
	assert.Equal(t, "id2", ev.ContainerID)
	assert.Equal(t, false, ev.Status, "stopped")
}

func TestEmit(t *testing.T) {
	client := &mockDockerClient{}
	time.Sleep(10 * time.Millisecond)

	client.add("id1", "name1")
	client.add("id2", "tst_exclude")
	client.add("id2", "name2")

	events, err := NewEventNotif(client, []string{"tst_exclude"}, []string{})
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "name1", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")

	ev = <-events.Channel()
	assert.Equal(t, "name2", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")
}

func TestEmitIncludes(t *testing.T) {
	client := &mockDockerClient{}
	time.Sleep(10 * time.Millisecond)

	client.add("id1", "name1")
	client.add("id2", "tst_include")
	client.add("id2", "name2")

	events, err := NewEventNotif(client, []string{}, []string{"tst_include"})
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "tst_include", ev.ContainerName)
	assert.Equal(t, true, ev.Status, "started")
}

func TestNewEventNotifWithNils(t *testing.T) {
	client := &mockDockerClient{}

	_, err := NewEventNotif(client, nil, nil)
	require.NoError(t, err)
}

func TestIsAllowedExclude(t *testing.T) {
	client := &mockDockerClient{}
	events, err := NewEventNotif(client, []string{"tst_exclude"}, nil)
	require.NoError(t, err)

	assert.True(t, events.isAllowed("name1"))
	assert.False(t, events.isAllowed("tst_exclude"))
}

func TestIsAllowedInclude(t *testing.T) {
	client := &mockDockerClient{}
	events, err := NewEventNotif(client, nil, []string{"tst_include"})
	require.NoError(t, err)

	assert.True(t, events.isAllowed("tst_include"))
	assert.False(t, events.isAllowed("name1"))
	assert.False(t, events.isAllowed("tst_exclude"))
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

	removingContainerName := m.getContainerName(id)

	r := []dockerclient.APIContainers{}
	for _, c := range m.containers {
		if c.ID != id {
			r = append(r, c)
		}
	}
	m.containers = r

	actor := dockerclient.APIActor{ID: id, Attributes: map[string]string{"name": removingContainerName}}
	ev := dockerclient.APIEvents{Type: "container", ID: id, Status: "stop", Actor: actor}
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

func (m *mockDockerClient) getContainerName(id string) string {
	for _, c := range m.containers {
		if id == c.ID {
			return c.Names[0]
		}
	}

	panic("Can't find container with specified id")
}
