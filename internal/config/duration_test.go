package config

import (
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

func TestDurationUnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"empty means zero", "", 0, false},
		{"literal zero means zero", "0", 0, false},
		{"thirty minutes", "30m", 30 * time.Minute, false},
		{"compound", "1h30m", 90 * time.Minute, false},
		{"milliseconds", "300ms", 300 * time.Millisecond, false},
		{"seconds", "5s", 5 * time.Second, false},
		{"invalid", "not-a-duration", 0, true},
		{"bare number invalid", "30", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var d Duration
			err := d.UnmarshalText([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.input, err)
			}
			if d.D() != tt.want {
				t.Errorf("input %q: got %v, want %v", tt.input, d.D(), tt.want)
			}
		})
	}
}

func TestDurationMarshalText(t *testing.T) {
	tests := []struct {
		name string
		d    Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"thirty minutes", Duration(30 * time.Minute), "30m0s"},
		{"300ms", Duration(300 * time.Millisecond), "300ms"},
		{"5s", Duration(5 * time.Second), "5s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := tt.d.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText: %v", err)
			}
			if string(b) != tt.want {
				t.Errorf("got %q, want %q", string(b), tt.want)
			}
		})
	}
}

func TestDurationIsZero(t *testing.T) {
	var z Duration
	if !z.IsZero() {
		t.Errorf("zero Duration should report IsZero()")
	}

	nz := Duration(time.Second)
	if nz.IsZero() {
		t.Errorf("non-zero Duration should not report IsZero()")
	}
}

// TestDurationTOMLRoundTrip verifies that durations survive a
// Marshal -> Unmarshal cycle as TOML strings (not nanosecond integers).
func TestDurationTOMLRoundTrip(t *testing.T) {
	type wrapper struct {
		Idle Duration `toml:"idle"`
	}

	original := wrapper{Idle: Duration(30 * time.Minute)}
	data, err := toml.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Sanity: emitted as a quoted string (single or double quotes,
	// depending on go-toml version), not a raw integer.
	got := string(data)
	if !contains(got, `"30m0s"`) && !contains(got, `'30m0s'`) {
		t.Errorf("expected duration to marshal as quoted string, got: %s", got)
	}

	var loaded wrapper
	if err := toml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded.Idle.D() != original.Idle.D() {
		t.Errorf("got %v, want %v", loaded.Idle.D(), original.Idle.D())
	}
}

// TestDurationTOMLZeroSentinels verifies that "" and "0" both decode
// to a zero duration (the "never exit" sentinel for IdleTimeout).
func TestDurationTOMLZeroSentinels(t *testing.T) {
	type wrapper struct {
		Idle Duration `toml:"idle"`
	}

	for _, raw := range []string{`idle = ""`, `idle = "0"`} {
		t.Run(raw, func(t *testing.T) {
			var w wrapper
			if err := toml.Unmarshal([]byte(raw), &w); err != nil {
				t.Fatalf("Unmarshal %q: %v", raw, err)
			}
			if !w.Idle.IsZero() {
				t.Errorf("expected zero, got %v", w.Idle.D())
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
