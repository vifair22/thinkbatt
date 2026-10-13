package battery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Set enumerates the battery packs present on the machine and opens each as a
// Battery. The daemon rescans a Set so packs that come and go at runtime (an
// external pack docked or pulled) are picked up without a restart. Sysfs is the
// real implementation; tests supply an in-memory fake.
type Set interface {
	// List returns the names of the packs present now, sorted (e.g. BAT0, BAT1).
	List() ([]string, error)
	// Open returns the Battery for a pack name. It does not check presence; the
	// caller lists first.
	Open(name string) Battery
}

// SysfsSet lists the power_supply tree's Battery-type supplies.
type SysfsSet struct{ root string }

// NewSysfsSet returns a SysfsSet. An empty root falls back to DefaultRoot.
func NewSysfsSet(root string) *SysfsSet {
	if root == "" {
		root = DefaultRoot
	}
	return &SysfsSet{root: root}
}

// List returns every supply under the root whose type is "Battery", sorted by
// name. Scanning by type (rather than a BAT* glob) is how acOnline finds Mains
// supplies too, and avoids assuming the kernel's naming.
func (s *SysfsSet) List() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", s.root, err)
	}
	var names []string
	for _, e := range entries {
		typ, err := readStr(filepath.Join(s.root, e.Name(), "type"))
		if err != nil || typ != "Battery" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Open returns a Sysfs bound to the named pack under the same root.
func (s *SysfsSet) Open(name string) Battery { return NewSysfs(s.root, name) }
