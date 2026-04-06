package operation

import (
	"context"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

// ProgressFunc is called by lifecycle methods to report phase changes and byte progress.
// Pass nil to callers that don't need progress tracking.
type ProgressFunc func(phase model.OperationPhase, progress *model.OperationProgress)

// OperationFunc is the work function passed to Submit.
type OperationFunc func(ctx context.Context, onProgress ProgressFunc) error

// Runner manages the lifecycle of async operations. It owns the operation guard
// (preventing concurrent operations), activity tracking (DB events), progress
// reporting, and the background goroutine. Used at the HTTP boundary to return
// "accepted" immediately while the work runs in the background.
type Runner struct {
	activity *ActivityTracker
	tracker  *Tracker
	log      *slog.Logger
	wg       sync.WaitGroup
}

func NewRunner(activity *ActivityTracker, tracker *Tracker, log *slog.Logger) *Runner {
	return &Runner{activity: activity, tracker: tracker, log: log}
}

// Submit validates the operation guard, records the activity, and runs fn in a
// background goroutine. Returns nil if the operation was accepted, or an error
// if it was rejected (e.g. another operation is already in progress).
//
// The runner creates a detached context with the actor set, so fn survives
// HTTP request cancellation. Progress callbacks are wired to the operation tracker.
func (r *Runner) Submit(gsID, opType string, actor event.Actor, fn OperationFunc) error {
	_, err := r.activity.Start(gsID, "", opType, nil, nil)
	if err != nil {
		return err
	}

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		ctx := event.SetActorInContext(context.Background(), actor)

		onProgress := func(phase model.OperationPhase, progress *model.OperationProgress) {
			r.tracker.SetOperation(gsID, opType, phase)
			if progress != nil {
				r.tracker.UpdateProgress(gsID, *progress)
			}
		}

		if err := fn(ctx, onProgress); err != nil {
			r.log.Error("operation failed", "gameserver", gsID, "operation", opType, "error", err)
			r.activity.Fail(gsID, err)
		} else {
			r.activity.Complete(gsID)
		}

		r.tracker.ClearOperation(gsID)
	}()

	return nil
}

// Wait blocks until all submitted operations complete. Intended for tests.
func (r *Runner) Wait() {
	r.wg.Wait()
}

// Tracker returns the operation tracker for read-only access.
func (r *Runner) Tracker() *Tracker {
	return r.tracker
}
