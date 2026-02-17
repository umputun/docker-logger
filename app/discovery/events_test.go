package discovery

import (
	"errors"
	"testing"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/umputun/docker-logger/app/discovery/mocks"
)

// makeListenerMock creates a mock with a synchronized event listener channel.
// returns the mock and a function to get the event channel (blocks until ready).
func makeListenerMock() (
	dMock *mocks.DockerClientMock, getEventsCh func() chan<- *dockerclient.APIEvents,
) {
	ready := make(chan chan<- *dockerclient.APIEvents, 1)
	dMock = &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			ready <- listener
			return nil
		},
	}
	return dMock, func() chan<- *dockerclient.APIEvents { return <-ready }
}

func TestEvents(t *testing.T) {
	mock, getEventsCh := makeListenerMock()

	events, err := NewEventNotif(mock, EventNotifOpts{Excludes: []string{"tst_exclude"}})
	require.NoError(t, err)
	eventsCh := getEventsCh()

	// send start event
	ev := &dockerclient.APIEvents{Type: "container", Status: "start"}
	ev.Actor.Attributes = map[string]string{"name": "name1"}
	ev.Actor.ID = "id1"
	eventsCh <- ev

	received := <-events.Channel()
	assert.Equal(t, "name1", received.ContainerName)
	assert.True(t, received.Status, "started")

	// send stop event
	ev = &dockerclient.APIEvents{Type: "container", ID: "id1", Status: "stop"}
	ev.Actor.Attributes = map[string]string{"name": "name1"}
	ev.Actor.ID = "id1"
	eventsCh <- ev

	received = <-events.Channel()
	assert.Equal(t, "id1", received.ContainerID)
	assert.False(t, received.Status, "stopped")

	assert.Len(t, mock.AddEventListenerCalls(), 1)
	assert.Len(t, mock.ListContainersCalls(), 1)
}

func TestEventsIncludes(t *testing.T) {
	mock, getEventsCh := makeListenerMock()

	events, err := NewEventNotif(mock, EventNotifOpts{Includes: []string{"tst_included"}})
	require.NoError(t, err)
	eventsCh := getEventsCh()

	ev := &dockerclient.APIEvents{Type: "container", Status: "start"}
	ev.Actor.Attributes = map[string]string{"name": "tst_included"}
	ev.Actor.ID = "id2"
	eventsCh <- ev

	received := <-events.Channel()
	assert.Equal(t, "tst_included", received.ContainerName)
	assert.True(t, received.Status, "started")

	// send stop
	ev = &dockerclient.APIEvents{Type: "container", Status: "stop"}
	ev.Actor.Attributes = map[string]string{"name": "tst_included"}
	ev.Actor.ID = "id2"
	eventsCh <- ev

	received = <-events.Channel()
	assert.Equal(t, "id2", received.ContainerID)
	assert.False(t, received.Status, "stopped")
}

func TestEmit(t *testing.T) {
	now := time.Now()
	containers := []dockerclient.APIContainers{
		{ID: "id1", Names: []string{"name1"}, Image: "docker.umputun.com/group1/img:latest", Created: now.Unix()},
		{ID: "id2", Names: []string{"tst_exclude"}, Image: "img:latest", Created: now.Unix()},
		{ID: "id3", Names: []string{"name2"}, Image: "docker.umputun.com/group2/img:latest", Created: now.Unix()},
	}

	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return containers, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{Excludes: []string{"tst_exclude"}})
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "name1", ev.ContainerName)
	assert.True(t, ev.Status, "started")
	assert.Equal(t, "group1", ev.Group)
	assert.WithinDuration(t, now, ev.TS, time.Second, "timestamp should be close to now")

	ev = <-events.Channel()
	assert.Equal(t, "name2", ev.ContainerName)
	assert.True(t, ev.Status, "started")
	assert.Equal(t, "group2", ev.Group)
	assert.WithinDuration(t, now, ev.TS, time.Second, "timestamp should be close to now")
}

func TestEmitSkipsContainersWithNoNames(t *testing.T) {
	containers := []dockerclient.APIContainers{
		{ID: "id1", Names: nil, Image: "img:latest"},
		{ID: "id2", Names: []string{"name2"}, Image: "img:latest", Created: time.Now().Unix()},
	}

	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return containers, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "name2", ev.ContainerName, "container with no names should be skipped")
}

func TestEmitIncludes(t *testing.T) {
	containers := []dockerclient.APIContainers{
		{ID: "id1", Names: []string{"name1"}, Image: "img:latest"},
		{ID: "id2", Names: []string{"tst_include"}, Image: "img:latest"},
		{ID: "id3", Names: []string{"name2"}, Image: "img:latest"},
	}

	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return containers, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{Includes: []string{"tst_include"}})
	require.NoError(t, err)

	ev := <-events.Channel()
	assert.Equal(t, "tst_include", ev.ContainerName)
	assert.True(t, ev.Status, "started")
}

func TestNewEventNotifWithNils(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}
	_, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)
}

func TestNewEventNotifInvalidIncludesPattern(t *testing.T) {
	mock := &mocks.DockerClientMock{}
	_, err := NewEventNotif(mock, EventNotifOpts{IncludesPattern: "[invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to compile includesPattern")
}

func TestNewEventNotifInvalidExcludesPattern(t *testing.T) {
	mock := &mocks.DockerClientMock{}
	_, err := NewEventNotif(mock, EventNotifOpts{ExcludesPattern: "[invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to compile excludesPattern")
}

func TestNewEventNotifListContainersError(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, errors.New("connection refused")
		},
	}
	_, err := NewEventNotif(mock, EventNotifOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to emit containers")
}

func TestActivateFiltersNonContainerEvents(t *testing.T) {
	mock, getEventsCh := makeListenerMock()

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)
	eventsCh := getEventsCh()

	// send non-container event (should be filtered)
	eventsCh <- &dockerclient.APIEvents{Type: "image", Status: "pull"}

	// send irrelevant container status (should be filtered)
	ev := &dockerclient.APIEvents{Type: "container", Status: "exec_start"}
	ev.Actor.Attributes = map[string]string{"name": "test"}
	ev.Actor.ID = "id1"
	eventsCh <- ev

	// send valid container start (should pass through)
	ev = &dockerclient.APIEvents{Type: "container", Status: "start"}
	ev.Actor.Attributes = map[string]string{"name": "valid"}
	ev.Actor.ID = "id2"
	eventsCh <- ev

	received := <-events.Channel()
	assert.Equal(t, "valid", received.ContainerName, "only valid container events should pass")
	assert.True(t, received.Status)
}

func TestActivateExcludedContainerFiltered(t *testing.T) {
	ready := make(chan chan<- *dockerclient.APIEvents, 1)
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			ready <- listener
			return nil
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{Excludes: []string{"excluded"}})
	require.NoError(t, err)
	eventsCh := <-ready

	// send excluded container event
	ev := &dockerclient.APIEvents{Type: "container", Status: "start"}
	ev.Actor.Attributes = map[string]string{"name": "excluded"}
	ev.Actor.ID = "id1"
	eventsCh <- ev

	// send allowed container event
	ev = &dockerclient.APIEvents{Type: "container", Status: "start"}
	ev.Actor.Attributes = map[string]string{"name": "allowed"}
	ev.Actor.ID = "id2"
	eventsCh <- ev

	received := <-events.Channel()
	assert.Equal(t, "allowed", received.ContainerName, "excluded containers should be filtered")
}

func TestActivateGroupFromImage(t *testing.T) {
	mock, getEventsCh := makeListenerMock()

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)
	eventsCh := getEventsCh()

	ev := &dockerclient.APIEvents{Type: "container", Status: "start", From: "docker.umputun.com:5500/radio-t/webstats:latest"}
	ev.Actor.Attributes = map[string]string{"name": "web"}
	ev.Actor.ID = "id1"
	eventsCh <- ev

	received := <-events.Channel()
	assert.Equal(t, "radio-t", received.Group)
}

func TestActivateAllContainerStatuses(t *testing.T) {
	mock, getEventsCh := makeListenerMock()

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)
	eventsCh := getEventsCh()

	tests := []struct {
		status   string
		expected bool
	}{
		{"start", true},
		{"restart", true},
		{"die", false},
		{"destroy", false},
		{"stop", false},
		{"pause", false},
	}

	for _, tt := range tests {
		ev := &dockerclient.APIEvents{Type: "container", Status: tt.status}
		ev.Actor.Attributes = map[string]string{"name": "c_" + tt.status}
		ev.Actor.ID = "id_" + tt.status
		eventsCh <- ev

		received := <-events.Channel()
		assert.Equal(t, tt.expected, received.Status, "status %s should map to %v", tt.status, tt.expected)
	}
}

func TestActivateTimestamp(t *testing.T) {
	t.Run("with TimeNano", func(t *testing.T) {
		mock, getEventsCh := makeListenerMock()
		events, err := NewEventNotif(mock, EventNotifOpts{})
		require.NoError(t, err)
		eventsCh := getEventsCh()

		now := time.Now()
		ev := &dockerclient.APIEvents{Type: "container", Status: "start"}
		ev.Actor.Attributes = map[string]string{"name": "ts_test"}
		ev.Actor.ID = "id1"
		ev.Time = now.Unix()
		ev.TimeNano = now.UnixNano()
		eventsCh <- ev

		received := <-events.Channel()
		assert.WithinDuration(t, now, received.TS, time.Second, "timestamp should use TimeNano precision")
	})

	t.Run("without TimeNano fallback to Time", func(t *testing.T) {
		mock, getEventsCh := makeListenerMock()
		events, err := NewEventNotif(mock, EventNotifOpts{})
		require.NoError(t, err)
		eventsCh := getEventsCh()

		now := time.Now()
		ev := &dockerclient.APIEvents{Type: "container", Status: "start"}
		ev.Actor.Attributes = map[string]string{"name": "ts_test2"}
		ev.Actor.ID = "id2"
		ev.Time = now.Unix()
		ev.TimeNano = 0 // simulate older Docker API
		eventsCh <- ev

		received := <-events.Channel()
		assert.WithinDuration(t, now, received.TS, time.Second, "timestamp should fall back to Time field")
	})
}

func TestIsAllowedExclude(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}
	events, err := NewEventNotif(mock, EventNotifOpts{Excludes: []string{"tst_exclude"}})
	require.NoError(t, err)

	assert.True(t, events.isAllowed("name1"))
	assert.False(t, events.isAllowed("tst_exclude"))
}

func TestIsAllowedExcludePattern(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}
	events, err := NewEventNotif(mock, EventNotifOpts{ExcludesPattern: "tst_exclude.*"})
	require.NoError(t, err)

	assert.True(t, events.isAllowed("tst_include"))
	assert.True(t, events.isAllowed("tst_include_yes"))
	assert.False(t, events.isAllowed("tst_exclude"))
	assert.False(t, events.isAllowed("tst_exclude_no"))
}

func TestIsAllowedInclude(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}
	events, err := NewEventNotif(mock, EventNotifOpts{Includes: []string{"tst_include"}})
	require.NoError(t, err)

	assert.True(t, events.isAllowed("tst_include"))
	assert.False(t, events.isAllowed("name1"))
	assert.False(t, events.isAllowed("tst_exclude"))
}

func TestIsAllowedIncludePattern(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return nil
		},
	}
	events, err := NewEventNotif(mock, EventNotifOpts{IncludesPattern: "tst_include.*"})
	require.NoError(t, err)

	assert.True(t, events.isAllowed("tst_include"))
	assert.True(t, events.isAllowed("tst_include_yes"))
	assert.False(t, events.isAllowed("tst_includ_no")) //nolint:misspell
	assert.False(t, events.isAllowed("tst_exclude_no"))
}

func TestGroup(t *testing.T) {
	d := EventNotif{}
	tbl := []struct {
		inp string
		out string
	}{
		{inp: "docker.umputun.com:5500/radio-t/webstats:latest", out: "radio-t"},
		{inp: "docker.umputun.com/some/webstats", out: "some"},
		{inp: "docker.umputun.com/some/blah/webstats", out: "some"},
		{inp: "docker.umputun.com/webstats:xxx", out: ""},
	}

	for _, tt := range tbl {
		assert.Equal(t, tt.out, d.group(tt.inp))
	}
}

func TestActivateAddEventListenerError(t *testing.T) {
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			return errors.New("listener error")
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)

	// eventsCh should be closed because AddEventListener failed
	_, ok := <-events.Channel()
	assert.False(t, ok, "events channel should be closed on AddEventListener error")
}

func TestActivateEventChannelClosed(t *testing.T) {
	ready := make(chan chan<- *dockerclient.APIEvents, 1)
	mock := &mocks.DockerClientMock{
		ListContainersFunc: func(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error) {
			return nil, nil
		},
		AddEventListenerFunc: func(listener chan<- *dockerclient.APIEvents) error {
			ready <- listener
			return nil
		},
	}

	events, err := NewEventNotif(mock, EventNotifOpts{})
	require.NoError(t, err)

	// close the docker events channel to simulate docker daemon disconnect
	dockerCh := <-ready
	close(dockerCh)

	// eventsCh should be closed because the docker events channel was closed
	_, ok := <-events.Channel()
	assert.False(t, ok, "events channel should be closed when docker events channel closes")
}
