package engine

import (
	"fmt"
	"strings"
)

// EventKind identifies a progress event.
type EventKind int

const (
	EventCreateRequested EventKind = iota // a create request was sent for Hosts
	EventCreateRetry                      // a create request failed transiently and will be retried
	EventCreated                          // the droplet for Hosts[0] exists, with DropletID
	EventReady                            // Hosts[0] is active and has an IP
	EventFailed                           // Hosts failed; see Err
	EventWarning                          // something worth noting that did not fail the run
	EventRollback                         // rollback started
	EventDeleted                          // the droplet for Hosts[0] was deleted during rollback
	EventProject                          // Hosts were moved into the project named Detail
)

// Event reports progress during a run. Reporters are called from multiple
// goroutines and must be safe for concurrent use.
type Event struct {
	Kind      EventKind
	Hosts     []string
	DropletID int
	Detail    string
	Err       error
}

// Reporter receives progress events.
type Reporter func(Event)

// String describes the event in one line.
func (e Event) String() string {
	hosts := strings.Join(e.Hosts, ", ")
	switch e.Kind {
	case EventCreateRequested:
		return fmt.Sprintf("creating %s (%s)", hosts, e.Detail)
	case EventCreateRetry:
		return fmt.Sprintf("create failed for %s, checking for partial creation then retrying: %v", hosts, e.Err)
	case EventCreated:
		return fmt.Sprintf("%s: droplet %d created, waiting for it to become active", hosts, e.DropletID)
	case EventReady:
		return fmt.Sprintf("%s: ready at %s", hosts, e.Detail)
	case EventFailed:
		if e.Detail != "" {
			return fmt.Sprintf("%s: FAILED (%s): %v", hosts, e.Detail, e.Err)
		}
		return fmt.Sprintf("%s: FAILED: %v", hosts, e.Err)
	case EventWarning:
		msg := "warning: " + e.Detail
		if hosts != "" {
			msg = fmt.Sprintf("warning: %s: %s", hosts, e.Detail)
		}
		if e.Err != nil {
			msg += fmt.Sprintf(": %v", e.Err)
		}
		return msg
	case EventRollback:
		return "rolling back: " + e.Detail
	case EventDeleted:
		return fmt.Sprintf("%s: droplet %d deleted", hosts, e.DropletID)
	case EventProject:
		return fmt.Sprintf("moved %s into project %s", hosts, e.Detail)
	}
	return fmt.Sprintf("event %d: %s", e.Kind, hosts)
}
