package event

import (
	"context"

	"github.com/warsmite/gamejanitor/controller/auth"
)

// Actor represents who/what initiated an action.
type Actor struct {
	Type       string `json:"type"`                  // "token", "schedule", "system", "anonymous"
	TokenID    string `json:"token_id,omitempty"`
	ScheduleID string `json:"schedule_id,omitempty"`
}

var SystemActor = Actor{Type: "system"}

type actorContextKey struct{}

// SetActorInContext stores an actor in the context for downstream event publishing.
func SetActorInContext(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFromContext extracts the actor from context.
// Returns anonymous actor if no actor is set (auth disabled).
func ActorFromContext(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorContextKey{}).(Actor); ok {
		return a
	}
	// Fall back to token-based actor for backward compatibility with auth middleware
	if token := auth.TokenFromContext(ctx); token != nil {
		return Actor{Type: "token", TokenID: token.ID}
	}
	return Actor{Type: "anonymous"}
}
