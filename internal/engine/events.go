package engine

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
