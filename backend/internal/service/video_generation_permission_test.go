//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupAllowsVideoGeneration(t *testing.T) {
	require.False(t, GroupAllowsVideoGeneration(nil))
	require.False(t, GroupAllowsVideoGeneration(&Group{}))
	require.True(t, GroupAllowsVideoGeneration(&Group{AllowVideoGeneration: true}))
}
