// Package prompts provides access to embedded prompt templates and skill definitions.
// Files are embedded from the embedded/ directory and can be overridden at runtime
// via the SetOverride function.
package prompts

import (
	"fmt"
	"os"
	"sync"

	"github.com/radvoogh/ralph-wiggo/embedded"
)

var (
	overrides   = make(map[string]string)
	overridesMu sync.RWMutex
)

// SetOverride registers a local file path to override an embedded file.
// When Get is called with the given name, the content of the local file
// at path is returned instead of the embedded content.
func SetOverride(name, path string) {
	overridesMu.Lock()
	defer overridesMu.Unlock()
	overrides[name] = path
}

// Get returns the content of the named prompt file. If an override has been
// set for the name, the override file is read from disk. Otherwise, the
// embedded file is returned.
func Get(name string) (string, error) {
	overridesMu.RLock()
	path, ok := overrides[name]
	overridesMu.RUnlock()

	if ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading override for %q: %w", name, err)
		}
		return string(data), nil
	}

	data, err := embedded.FS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("reading embedded file %q: %w", name, err)
	}
	return string(data), nil
}
