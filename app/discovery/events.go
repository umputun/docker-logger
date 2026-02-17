package discovery

import (
	"regexp"
	"slices"
	"strings"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	log "github.com/go-pkgz/lgr"
	"github.com/pkg/errors"
)

// EventNotif emits all changes from all containers states
type EventNotif struct {
	dockerClient   DockerClient
	excludes       []string
	includes       []string
	includesRegexp *regexp.Regexp
	excludesRegexp *regexp.Regexp
	eventsCh       chan Event
	listenerErr    chan error // communicates activate() failure back to the caller
}

// Event is simplified docker.APIEvents for containers only, exposed to caller
type Event struct {
	ContainerID   string
	ContainerName string
	Group         string // group is the "path" part of the image tag, i.e. for umputun/system/logger:latest it will be "system"
	TS            time.Time
	Status        bool
}

//go:generate moq -out mocks/docker_client.go -pkg mocks -skip-ensure -fmt goimports . DockerClient

// DockerClient defines interface listing containers and subscribing to events
type DockerClient interface {
	ListContainers(opts docker.ListContainersOptions) ([]docker.APIContainers, error)
	AddEventListener(listener chan<- *docker.APIEvents) error
}

var reGroup = regexp.MustCompile(`/(.*?)/`)

// EventNotifOpts contains options for NewEventNotif
type EventNotifOpts struct {
	Excludes        []string
	Includes        []string
	IncludesPattern string
	ExcludesPattern string
}

// NewEventNotif makes EventNotif publishing all changes to eventsCh
func NewEventNotif(dockerClient DockerClient, opts EventNotifOpts) (*EventNotif, error) {
	log.Printf("[DEBUG] create events notif, excludes: %+v, includes: %+v, includesPattern: %+v, excludesPattern: %+v",
		opts.Excludes, opts.Includes, opts.IncludesPattern, opts.ExcludesPattern)

	var err error
	var includesRe *regexp.Regexp
	if opts.IncludesPattern != "" {
		includesRe, err = regexp.Compile(opts.IncludesPattern)
		if err != nil {
			return nil, errors.Wrap(err, "failed to compile includesPattern")
		}
	}

	var excludesRe *regexp.Regexp
	if opts.ExcludesPattern != "" {
		excludesRe, err = regexp.Compile(opts.ExcludesPattern)
		if err != nil {
			return nil, errors.Wrap(err, "failed to compile excludesPattern")
		}
	}

	res := EventNotif{
		dockerClient:   dockerClient,
		excludes:       opts.Excludes,
		includes:       opts.Includes,
		includesRegexp: includesRe,
		excludesRegexp: excludesRe,
		eventsCh:       make(chan Event, 100),
		listenerErr:    make(chan error, 1),
	}

	// first get all currently running containers
	if err := res.emitRunningContainers(); err != nil {
		return nil, errors.Wrap(err, "failed to emit containers")
	}

	go func() {
		res.activate(dockerClient) // activate listener for new container events
	}()

	return &res, nil
}

// Channel gets eventsCh with all containers events
func (e *EventNotif) Channel() (res <-chan Event) {
	return e.eventsCh
}

// Err returns a channel that receives an error if the event listener fails to start.
// the channel is buffered (size 1) and will receive at most one error.
func (e *EventNotif) Err() <-chan error {
	return e.listenerErr
}

// activate starts blocking listener for all docker events
// filters everything except "container" type, detects stop/start events and publishes to eventsCh.
// on failure or channel close, it closes eventsCh to signal consumers.
func (e *EventNotif) activate(client DockerClient) {
	dockerEventsCh := make(chan *docker.APIEvents)
	if err := client.AddEventListener(dockerEventsCh); err != nil {
		log.Printf("[ERROR] can't add event listener, %v", err)
		e.listenerErr <- errors.Wrap(err, "can't add event listener")
		close(e.eventsCh)
		return
	}

	upStatuses := []string{"start", "restart"}
	downStatuses := []string{"die", "destroy", "stop", "pause"}

	for dockerEvent := range dockerEventsCh {
		if dockerEvent.Type != "container" {
			continue
		}

		if !slices.Contains(upStatuses, dockerEvent.Status) && !slices.Contains(downStatuses, dockerEvent.Status) {
			continue
		}

		log.Printf("[DEBUG] api event %+v", dockerEvent)
		containerName := strings.TrimPrefix(dockerEvent.Actor.Attributes["name"], "/")

		if !e.isAllowed(containerName) {
			log.Printf("[INFO] container %s excluded", containerName)
			continue
		}

		ts := time.Unix(0, dockerEvent.TimeNano)
		if dockerEvent.TimeNano == 0 {
			ts = time.Unix(dockerEvent.Time, 0)
		}

		event := Event{
			ContainerID:   dockerEvent.Actor.ID,
			ContainerName: containerName,
			Status:        slices.Contains(upStatuses, dockerEvent.Status),
			TS:            ts,
			Group:         e.group(dockerEvent.From),
		}
		log.Printf("[INFO] new event %+v", event)
		e.eventsCh <- event
	}
	log.Printf("[WARN] event listener closed")
	close(e.eventsCh)
}

// emitRunningContainers gets all currently running containers and publishes them as "Status=true" (started) events
func (e *EventNotif) emitRunningContainers() error {
	containers, err := e.dockerClient.ListContainers(docker.ListContainersOptions{All: false})
	if err != nil {
		return errors.Wrap(err, "can't list containers")
	}
	log.Printf("[DEBUG] total containers = %d", len(containers))

	for _, c := range containers {
		if len(c.Names) == 0 {
			log.Printf("[WARN] container %s has no names, skipped", c.ID)
			continue
		}
		containerName := strings.TrimPrefix(c.Names[0], "/")
		if !e.isAllowed(containerName) {
			log.Printf("[INFO] container %s excluded", containerName)
			continue
		}
		event := Event{
			Status:        true,
			ContainerName: containerName,
			ContainerID:   c.ID,
			TS:            time.Unix(c.Created, 0),
			Group:         e.group(c.Image),
		}
		log.Printf("[DEBUG] running container added, %+v", event)
		e.eventsCh <- event
	}
	log.Print("[DEBUG] completed initial emit")
	return nil
}

func (e *EventNotif) group(image string) string {
	if r := reGroup.FindStringSubmatch(image); len(r) == 2 {
		return r[1]
	}
	log.Printf("[DEBUG] no group for %s", image)
	return ""
}

func (e *EventNotif) isAllowed(containerName string) bool {
	if e.includesRegexp != nil {
		return e.includesRegexp.MatchString(containerName)
	}
	if e.excludesRegexp != nil {
		return !e.excludesRegexp.MatchString(containerName)
	}
	if len(e.includes) > 0 {
		return slices.Contains(e.includes, containerName)
	}
	if slices.Contains(e.excludes, containerName) {
		return false
	}

	return true
}
