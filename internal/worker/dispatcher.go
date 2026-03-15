package worker

// Dispatcher routes operations to the correct Worker for a given gameserver.
// In standalone mode, all operations go to a single LocalWorker.
// Multi-node will route based on gameserver-to-node assignment.
type Dispatcher struct {
	worker Worker
}

func NewLocalDispatcher(w Worker) *Dispatcher {
	return &Dispatcher{worker: w}
}

// WorkerFor returns the Worker responsible for an existing gameserver.
func (d *Dispatcher) WorkerFor(gameserverID string) Worker {
	return d.worker
}

// DefaultWorker returns the Worker for new gameservers (placement decisions).
func (d *Dispatcher) DefaultWorker() Worker {
	return d.worker
}
