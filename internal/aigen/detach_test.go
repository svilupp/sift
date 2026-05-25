package aigen

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStripDetachFlag(t *testing.T) {
	in := []string{"refresh", "--index-only", "--detach", "--generate=stale"}
	out := stripDetachFlag(in)
	want := []string{"refresh", "--index-only", "--generate=stale"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("got %v want %v", out, want)
	}

	in2 := []string{"refresh", "--detach=true", "x"}
	if got := stripDetachFlag(in2); !reflect.DeepEqual(got, []string{"refresh", "x"}) {
		t.Errorf("got %v", got)
	}
}

func TestReadPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pid")
	// Missing file → error.
	if _, err := ReadPID(path); err == nil {
		t.Errorf("expected error on missing file")
	}
}

func TestIsAliveBogus(t *testing.T) {
	if IsAlive(-1) {
		t.Errorf("IsAlive(-1) should be false")
	}
	// Some impossibly-high PID; should not be alive.
	if IsAlive(2_147_483_640) {
		t.Errorf("IsAlive(huge pid) should be false")
	}
}
