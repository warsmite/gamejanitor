//go:build e2e

package e2e

// files_test.go — file manager operations against a gameserver volume.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFiles_WriteRead(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()

	// Start so the volume exists and is populated.
	gs.Start().MustBeRunning()

	gs.WriteFile("/data/test.txt", []byte("hello from e2e"))
	content := gs.ReadFile("/data/test.txt")

	assert.True(t, strings.Contains(content, "hello from e2e"),
		"readback should contain written content; got %q", content)
}

func TestFiles_ListDirectory(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()
	gs.Start().MustBeRunning()

	entries := gs.ListFiles("/data")
	assert.NotEmpty(t, entries, "volume should have files after install")
}
