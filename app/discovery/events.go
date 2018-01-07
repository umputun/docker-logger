package discovery

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/fsouza/go-dockerclient"
)

// EventNotif emits all changes from all containers states
type EventNotif struct {
	dockerClient *docker.Client
	excludes     []string
	eventsCh     chan Event
}

// Event is simplified docker.APIEvents for containers only exposed to caller
type Event struct {
	ContainerID   string
	ContainerName string
	Group         string
	TS            time.Time
	Status        bool
}

var reGroup = regexp.MustCompile(`/(.*?)/`)

// NewEventNotif makes EventNotif publishing all changes to eventsCh
func NewEventNotif(dockerClient *docker.Client, excludes []string) (*EventNotif, error) {
	log.Printf("[DEBUG] create events notif for %s, excludes: %+v", dockerClient.Endpoint(), excludes)
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
func (e *EventNotif) activate(client *docker.Client) {
	dockerEventsCh := make(chan *docker.APIEvents)
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

// emitRunningContainers gets all current containers and publishes them as "Status=true" (started) events
func (e *EventNotif) emitRunningContainers() error {

	containers, err := e.dockerClient.ListContainers(docker.ListContainersOptions{All: false})
	if err != nil {
		return err
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
