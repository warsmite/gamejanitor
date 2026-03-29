package naming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNaming_ContainerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		gameserverID string
		expected     string
	}{
		{"abc-123", "gamejanitor-abc-123"},
		{"my-server", "gamejanitor-my-server"},
		{"550e8400-e29b-41d4-a716-446655440000", "gamejanitor-550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.gameserverID, func(t *testing.T) {
			assert.Equal(t, tt.expected, ContainerName(tt.gameserverID))
		})
	}

	// Update container names use a distinct prefix
	assert.Equal(t, "gamejanitor-update-abc-123", UpdateContainerName("abc-123"))
}

func TestNaming_VolumeName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "gamejanitor-abc-123", VolumeName("abc-123"))
	assert.Equal(t, "gamejanitor-my-server", VolumeName("my-server"))
}

func TestNaming_GameserverIDFromContainerName_RoundTrip(t *testing.T) {
	t.Parallel()

	ids := []string{"abc-123", "my-server", "550e8400-e29b-41d4-a716-446655440000"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			containerName := ContainerName(id)
			extracted, ok := GameserverIDFromContainerName(containerName)
			require.True(t, ok)
			assert.Equal(t, id, extracted)
		})
	}
}

func TestNaming_GameserverIDFromContainerName_RejectsNonGameserver(t *testing.T) {
	t.Parallel()

	t.Run("no prefix", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("some-other-container")
		assert.False(t, ok)
	})

	t.Run("empty string", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("")
		assert.False(t, ok)
	})

	t.Run("update container", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("gamejanitor-update-abc-123")
		assert.False(t, ok)
	})

	t.Run("fileops container", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("gamejanitor-fileops-vol-123")
		assert.False(t, ok)
	})

	t.Run("backup container", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("gamejanitor-backup-abc-123")
		assert.False(t, ok)
	})

	t.Run("reinstall container", func(t *testing.T) {
		_, ok := GameserverIDFromContainerName("gamejanitor-reinstall-abc-123")
		assert.False(t, ok)
	})
}
