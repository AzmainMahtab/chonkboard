package idgenerator

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewUUIDv7IsCanonicalAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := NewUUIDv7()
		require.Len(t, id, 36)
		assert.True(t, Valid(id), "%q should be valid", id)
		assert.Equal(t, byte('7'), id[14], "version nibble should be 7: %s", id)

		_, dup := seen[id]
		require.False(t, dup, "duplicate id %s", id)
		seen[id] = struct{}{}
	}
}

func TestNewUUIDv7SortsRoughlyByCreationOrder(t *testing.T) {
	// The property that makes these usable as primary keys: successive inserts
	// land next to each other in the index instead of scattering.
	const n = 200
	ids := make([]string, n)
	for i := range ids {
		ids[i] = NewUUIDv7()
	}

	sorted := make([]string, n)
	copy(sorted, ids)
	sort.Strings(sorted)

	assert.Equal(t, ids, sorted, "v7 ids should already be in ascending order")
}

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"generated id", NewUUIDv7(), true},
		{"canonical v4", "9f1d4b8e-3c2a-4f6b-9e0d-1a2b3c4d5e6f", true},
		{"empty", "", false},
		{"not a uuid", "backlog", false},
		{"braced form is not canonical", "{9f1d4b8e-3c2a-4f6b-9e0d-1a2b3c4d5e6f}", false},
		{"urn form is not canonical", "urn:uuid:9f1d4b8e-3c2a-4f6b-9e0d-1a2b3c4d5e6f", false},
		{"upper case is not canonical", "9F1D4B8E-3C2A-4F6B-9E0D-1A2B3C4D5E6F", false},
		{"missing dashes is not canonical", "9f1d4b8e3c2a4f6b9e0d1a2b3c4d5e6f", false},
		{"sql injection attempt", "' OR 1=1 --", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Valid(tc.in))
		})
	}
}
