package discovery

import (
	"log"
	"regexp"
	"strings"
	"time"

	dockerclient "github.com/fsouza/go-dockerclient"
	"github.com/pkg/errors"
)

// EventNotif emits all changes from all containers states
type EventNotif struct {
	dockerClient DockerClient
	excludes     []string
	eventsCh     chan Event
}

// Event is simplified docker.APIEvents for containers only, exposed to caller
type Event struct {
	ContainerID   string
	ContainerName string
	Group         string // group is the "path" part of the image tag, i.e. for umputun/system/logger:latest it will be "system"
	TS            time.Time
	Status        bool
}

// DockerClient defines interface listing containers and subscribing to events
type DockerClient interface {
	ListContainers(opts dockerclient.ListContainersOptions) ([]dockerclient.APIContainers, error)
	AddEventListener(listener chan<- *dockerclient.APIEvents) error
}

var reGroup = regexp.MustCompile(`/(.*?)/`)

// NewEventNotif makes EventNotif publishing all changes to eventsCh
func NewEventNotif(dockerClient DockerClient, excludes ...string) (*EventNotif, error) {
	log.Printf("[DEBUG] create events notif, excludes: %+v", excludes)
	res := EventNotif{
		dockerClient: dockerClient,
		excludes:     excludes,
		eventsCh:     make(chan Event, 100),
	}
	go func() {
		// first get all currently running containers
		if err := res.emitRunningContainers(); err != nil {
			log.Fatalf("[ERROR] failed to emit containers, %v", err)
		}
		res.activate(dockerClient) // activate listener for new container events
	}()

	return &res, nil
}

// Channel gets eventsCh with all containers events
func (e *EventNotif) Channel() (res <-chan Event) {
	return e.eventsCh
}

// activate starts blocking listener for all docker events
// filters everything except "container" type, detects stop/start events and publishes to eventsCh
func (e *EventNotif) activate(client DockerClient) {
	dockerEventsCh := make(chan *dockerclient.APIEvents)
	if err := client.AddEventListener(dockerEventsCh); err != nil {
		log.Fatalf("[ERROR] can't add even listener, %v", err)
	}

	upStatuses := []string{"start", "restart"}
	downStatuses := []string{"die", "destroy", "stop", "pause"}

	for dockerEvent := range dockerEventsCh {
		if dockerEvent.Type == "container" {

			if !contains(dockerEvent.Status, upStatuses) && !contains(dockerEvent.Status, downStatuses) {
				continue
			}

			log.Printf("[DEBUG] api event %+v", dockerEvent)
			containerName := strings.TrimPrefix(dockerEvent.Actor.Attributes["name"], "/")
			if contains(containerName, e.excludes) {
				log.Printf("[INFO] container %s excluded", containerName)
				continue
			}

			event := Event{
				ContainerID:   dockerEvent.Actor.ID,
				ContainerName: containerName,
				Status:        contains(dockerEvent.Status, upStatuses),
				TS:            time.Unix(dockerEvent.Time/1000, dockerEvent.TimeNano),
				Group:         e.group(dockerEvent.From),
			}
			log.Printf("[INFO] new event %+v", event)
			e.eventsCh <- event
		}
	}
	log.Fatal("[ERROR] event listener failed")
}

// emitRunningContainers gets all currently running containers and publishes them as "Status=true" (started) events
func (e *EventNotif) emitRunningContainers() error {

	containers, err := e.dockerClient.ListContainers(dockerclient.ListContainersOptions{All: false})
	if err != nil {
		return errors.Wrap(err, "can't list containers")
	}
	log.Printf("[DEBUG] total containers = %d", len(containers))

	for _, c := range containers {
		containerName := strings.TrimPrefix(c.Names[0], "/")
		if contains(containerName, e.excludes) {
			log.Printf("[INFO] container %s excluded", containerName)
			continue
		}
		event := Event{
			Status:        true,
			ContainerName: containerName,
			ContainerID:   c.ID,
			TS:            time.Unix(c.Created/1000, 0),
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

func contains(e string, s []string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
