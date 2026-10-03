// Package bridgestest builds the reference bridge plugin, cmd/park-bridge-file, for tests that run a real plugin.
package bridgestest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Build builds the reference plugin into a new temporary directory, and returns the executable's path and a function
// that removes the directory. A package's TestMain calls it once, since building takes a moment.
func Build() (string, func(), error) {
	dir, err := os.MkdirTemp("", "park-plugins-")
	if err != nil {
		return "", nil, err
	}
	done := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "park-bridge-file")
	build := exec.CommandContext(context.Background(), "go", "build", "-o", path, "github.com/NickWilde18/Park/cmd/park-bridge-file")
	if out, err := build.CombinedOutput(); err != nil {
		done()
		return "", nil, fmt.Errorf("build the reference plugin: %w: %s", err, out)
	}
	return path, done, nil
}

// Dir returns a new directory, removed when the test ends, holding only the plugin at path: a plugin directory.
func Dir(t interface {
	TempDir() string
	Fatal(...any)
}, path string) string {
	dir := t.TempDir()
	if err := os.Symlink(path, filepath.Join(dir, filepath.Base(path))); err != nil {
		t.Fatal(err)
	}
	return dir
}
