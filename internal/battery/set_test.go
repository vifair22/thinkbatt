package battery

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeSupply creates a power_supply-style directory with a type file.
func writeSupply(t *testing.T, root, name, typ string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "type"), []byte(typ+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeScope adds a scope attribute to an existing supply directory.
func writeScope(t *testing.T, root, name, scope string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name, "scope"), []byte(scope+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSysfsSetList(t *testing.T) {
	root := t.TempDir()
	// Two packs, a mains supply, and a typeless directory: only the Battery-type
	// entries should be listed, and in sorted order.
	writeSupply(t, root, "BAT1", "Battery")
	writeSupply(t, root, "BAT0", "Battery")
	writeSupply(t, root, "AC", "Mains")
	if err := os.MkdirAll(filepath.Join(root, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewSysfsSet(root)
	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"BAT0", "BAT1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}

	// Open returns a Battery bound to the named pack under the same root.
	b := s.Open("BAT0")
	if sb, ok := b.(*Sysfs); !ok || sb.bat != "BAT0" || sb.root != root {
		t.Errorf("Open did not bind to BAT0 under root: %+v", b)
	}
}

func TestSysfsSetListScope(t *testing.T) {
	root := t.TempDir()
	// A laptop pack with no scope (kept), one explicitly scope=System (kept), and
	// a peripheral battery (a Bluetooth mouse) with scope=Device (dropped). Only
	// the two system packs should be listed.
	writeSupply(t, root, "BAT0", "Battery")
	writeSupply(t, root, "BAT1", "Battery")
	writeScope(t, root, "BAT1", "System")
	writeSupply(t, root, "hidpp_battery_0", "Battery")
	writeScope(t, root, "hidpp_battery_0", "Device")

	got, err := NewSysfsSet(root).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"BAT0", "BAT1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v (scope=Device peripheral should be dropped)", got, want)
	}
}

func TestSysfsSetListMissingRoot(t *testing.T) {
	s := NewSysfsSet(filepath.Join(t.TempDir(), "absent"))
	if _, err := s.List(); err == nil {
		t.Errorf("List of a missing root should error")
	}
}

func TestNewSysfsSetDefaultRoot(t *testing.T) {
	if s := NewSysfsSet(""); s.root != DefaultRoot {
		t.Errorf("empty root = %q, want %q", s.root, DefaultRoot)
	}
}
