package database

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// Layout is the single wire format for every timestamp column in the schema.
//
// It is fixed-width and always UTC, and both of those matter. SQLite has no
// timestamp type: a TIMESTAMPTZ column takes NUMERIC affinity and these values
// land as TEXT, so ORDER BY created_at is a lexicographic comparison. That is
// only chronological if every value is the same shape. time.RFC3339Nano trims
// trailing zeros from the fraction, and '.' (0x2E) sorts before 'Z' (0x5A), so
// "…:53.5Z" would sort BEFORE "…:53Z" — the rows would come back in the wrong
// order with no error anywhere.
//
// Six fractional digits is also exactly PostgreSQL's timestamptz precision, so
// the same strings move across without rounding.
const Layout = "2006-01-02T15:04:05.000000Z"

// Time is the type every model uses for a NOT NULL timestamp column. The
// driver will not do this for us: modernc.org/sqlite does not recognise a
// TIMESTAMPTZ declared type, so handing it a bare time.Time stores Go's
// time.String() output ("2026-09-25 19:51:53.518063 +0000 UTC") which cannot be
// read back and is not valid PostgreSQL input either.
type Time struct {
	t time.Time
}

// NewTime normalises to UTC and truncates to the precision the column holds, so
// that a value written and read back compares equal.
func NewTime(t time.Time) Time {
	return Time{t: t.UTC().Truncate(time.Microsecond)}
}

// Now is the only clock the stores use.
func Now() Time { return NewTime(time.Now()) }

// Time returns the wrapped instant, always in UTC.
func (t Time) Time() time.Time { return t.t }

// IsZero reports whether the instant is the zero time.
func (t Time) IsZero() bool { return t.t.IsZero() }

// String renders the stored form, which is also what a log line wants.
func (t Time) String() string { return t.t.Format(Layout) }

// Value implements driver.Valuer.
func (t Time) Value() (driver.Value, error) { return t.t.Format(Layout), nil }

// Scan implements sql.Scanner. It accepts a string or []byte because that is
// what SQLite returns, and a time.Time because that is what a PostgreSQL driver
// will return from a real timestamptz column — so the models do not change when
// the dialect does.
func (t *Time) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		return fmt.Errorf("database: cannot scan NULL into a Time; use NullTime")
	case time.Time:
		*t = NewTime(v)
		return nil
	case string:
		return t.parse(v)
	case []byte:
		return t.parse(string(v))
	default:
		return fmt.Errorf("database: cannot scan %T into Time", src)
	}
}

// parseLayouts is ordered most-likely first. The extra entries exist because a
// value may predate a format change, or come from a hand-written INSERT, and
// silently reading those as the zero time would be worse than a slow path.
var parseLayouts = []string{
	Layout,
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999-07:00",
	"2006-01-02 15:04:05.999999Z07:00",
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func (t *Time) parse(s string) error {
	for _, layout := range parseLayouts {
		if parsed, err := time.Parse(layout, s); err == nil {
			*t = NewTime(parsed)
			return nil
		}
	}
	return fmt.Errorf("database: %q is not a recognised timestamp", s)
}

// NullTime is Time for a nullable column. Every optional instant in the schema
// -- revoked_at, archived_at, due_at, deleted_at -- uses it.
type NullTime struct {
	T     Time
	Valid bool
}

// NewNullTime wraps a present instant.
func NewNullTime(t time.Time) NullTime { return NullTime{T: NewTime(t), Valid: true} }

// NullTimeFrom converts a possibly-nil instant.
func NullTimeFrom(t *time.Time) NullTime {
	if t == nil {
		return NullTime{}
	}
	return NewNullTime(*t)
}

// Ptr returns nil when the column was NULL, which is the shape domain types use.
func (n NullTime) Ptr() *time.Time {
	if !n.Valid {
		return nil
	}
	out := n.T.Time()
	return &out
}

// Value implements driver.Valuer.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.T.Value()
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(src any) error {
	if src == nil {
		*n = NullTime{}
		return nil
	}
	if err := n.T.Scan(src); err != nil {
		return err
	}
	n.Valid = true
	return nil
}
