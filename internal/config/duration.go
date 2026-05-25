package config

import (
	"fmt"
	"time"
)

// Duration is a time.Duration wrapper that marshals to/from a TOML string
// like "30m". Empty string and "0" are interpreted as zero (semantic
// "never" / "disabled" depending on the field).
type Duration time.Duration

// D returns the underlying time.Duration.
func (d Duration) D() time.Duration {
	return time.Duration(d)
}

// IsZero reports whether the duration is zero.
func (d Duration) IsZero() bool {
	return time.Duration(d) == 0
}

// String renders the duration via time.Duration.String. Zero renders as "0".
func (d Duration) String() string {
	return time.Duration(d).String()
}

// MarshalText implements encoding.TextMarshaler so go-toml emits a string
// literal like "30m" rather than an integer nanosecond count.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler. Accepts:
//   - "" (empty) -> 0
//   - "0"        -> 0
//   - any string parseable by time.ParseDuration ("30m", "5s", "1h30m", ...)
func (d *Duration) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" || s == "0" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}
