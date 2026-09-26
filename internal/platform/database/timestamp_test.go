package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTimeWireFormatIsFixedWidth(t *testing.T) {
	// Every rendered timestamp must be the same length, or SQLite's
	// lexicographic ORDER BY stops being chronological.
	instants := []time.Time{
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),                // no fraction
		time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.UTC),      // .5
		time.Date(2026, 1, 2, 3, 4, 5, 123_456_000, time.UTC),      // .123456
		time.Date(2026, 12, 31, 23, 59, 59, 999_999_000, time.UTC), // max
	}

	width := len(NewTime(instants[0]).String())
	for _, in := range instants {
		got := NewTime(in).String()
		assert.Len(t, got, width, "width drifted for %s", in)
		assert.True(t, got[len(got)-1] == 'Z', "must end in Z: %s", got)
	}
	assert.Equal(t, "2026-01-02T03:04:05.000000Z", NewTime(instants[0]).String())
	assert.Equal(t, "2026-01-02T03:04:05.500000Z", NewTime(instants[1]).String())
}

func TestTimeStringSortMatchesChronologicalSort(t *testing.T) {
	// The exact trap RFC3339Nano would walk into: '.' (0x2E) sorts before
	// 'Z' (0x5A), so a trimmed fraction would sort before no fraction at all.
	earlier := NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	later := NewTime(time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.UTC))

	require.True(t, earlier.Time().Before(later.Time()), "fixture is wrong")
	assert.Less(t, earlier.String(), later.String(),
		"string order must agree with time order")

	trimmed := later.Time().Format(time.RFC3339Nano)
	untrimmed := earlier.Time().Format(time.RFC3339Nano)
	assert.Less(t, trimmed, untrimmed,
		"RFC3339Nano is expected to sort wrongly here; that is why Layout exists")
}

func TestTimeNormalisesToUTCAndMicroseconds(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*60*60)
	in := time.Date(2026, 3, 4, 10, 0, 0, 123_456_789, zone)

	got := NewTime(in)

	assert.Equal(t, time.UTC, got.Time().Location())
	assert.True(t, got.Time().Equal(in.Truncate(time.Microsecond)))
	assert.Equal(t, 123_456_000, got.Time().Nanosecond(), "truncated to microseconds")
}

func TestTimeRoundTripsThroughValueAndScan(t *testing.T) {
	original := NewTime(time.Date(2026, 5, 6, 7, 8, 9, 654_321_000, time.UTC))

	v, err := original.Value()
	require.NoError(t, err)

	var back Time
	require.NoError(t, back.Scan(v))
	assert.True(t, original.Time().Equal(back.Time()), "%s != %s", original, back)
}

func TestTimeScanAcceptsWhatEitherDriverReturns(t *testing.T) {
	want := time.Date(2026, 5, 6, 7, 8, 9, 654_321_000, time.UTC)

	tests := []struct {
		name string
		src  any
	}{
		{"our own layout as string", "2026-05-06T07:08:09.654321Z"},
		{"our own layout as bytes", []byte("2026-05-06T07:08:09.654321Z")},
		// A PostgreSQL driver hands back a real time.Time.
		{"time.Time from a pg driver", want},
		{"rfc3339 with offset", "2026-05-06T12:08:09.654321+05:00"},
		{"space separator", "2026-05-06 07:08:09.654321"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got Time
			require.NoError(t, got.Scan(tc.src))
			assert.True(t, want.Equal(got.Time()), "want %s got %s", want, got)
		})
	}
}

func TestTimeScanRejectsNullAndNonsense(t *testing.T) {
	var got Time
	assert.ErrorContains(t, got.Scan(nil), "use NullTime")
	assert.ErrorContains(t, got.Scan("not a date"), "not a recognised timestamp")
	assert.ErrorContains(t, got.Scan(42), "cannot scan int")
}

func TestNullTime(t *testing.T) {
	t.Run("absent value is NULL on the wire", func(t *testing.T) {
		v, err := NullTime{}.Value()
		require.NoError(t, err)
		assert.Nil(t, v)
	})

	t.Run("absent value scans back absent", func(t *testing.T) {
		var got NullTime
		require.NoError(t, got.Scan(nil))
		assert.False(t, got.Valid)
		assert.Nil(t, got.Ptr())
	})

	t.Run("present value round-trips", func(t *testing.T) {
		want := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
		v, err := NewNullTime(want).Value()
		require.NoError(t, err)

		var got NullTime
		require.NoError(t, got.Scan(v))
		require.True(t, got.Valid)
		assert.True(t, want.Equal(*got.Ptr()))
	})

	t.Run("NullTimeFrom handles a nil pointer", func(t *testing.T) {
		assert.False(t, NullTimeFrom(nil).Valid)
		now := time.Now()
		assert.True(t, NullTimeFrom(&now).Valid)
	})
}
