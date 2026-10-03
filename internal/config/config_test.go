package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const key = "0123456789abcdef0123456789abcdef" // 32 bytes

// env returns a getenv over a fixed set of variables.
func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// writeFile writes a configuration file into a temporary directory and returns its path.
func writeFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "park.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// With no file, the environment alone configures Park, and the listen address has its default.
func TestLoadFromEnvironment(t *testing.T) {
	c, err := Load("", env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": key}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.URL != "postgres://db/park" || c.SecretKey != key || c.HTTP.Addr != ":8080" {
		t.Fatalf("got %+v", c)
	}
}

// Every key the file sets is loaded.
func TestLoadFromFile(t *testing.T) {
	path := writeFile(t, "http:\n  addr: 127.0.0.1:9000\ndatabase:\n  url: postgres://file/park\nbridges:\n  plugin_dir: /opt/park/plugins\n"+
		"secret_key: "+key+"\n")
	c, err := Load(path, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTP.Addr != "127.0.0.1:9000" || c.Database.URL != "postgres://file/park" || c.Bridges.PluginDir != "/opt/park/plugins" ||
		c.SecretKey != key {
		t.Fatalf("got %+v", c)
	}
}

// The plugin directory has its own variable, and has none by default: Park then has no plugins.
func TestPluginDir(t *testing.T) {
	c, err := Load("", env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": key}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bridges.PluginDir != "" {
		t.Fatalf("bridges.plugin_dir = %q by default, want none", c.Bridges.PluginDir)
	}
	c, err = Load("", env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": key,
		"PARK_BRIDGES_PLUGIN_DIR": "/opt/park/plugins"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bridges.PluginDir != "/opt/park/plugins" {
		t.Fatalf("bridges.plugin_dir = %q, want the variable's", c.Bridges.PluginDir)
	}
}

// A variable in the environment wins over the same key in the file; keys it does not set keep the file's value.
func TestEnvironmentWinsOverFile(t *testing.T) {
	path := writeFile(t, "http:\n  addr: 127.0.0.1:9000\ndatabase:\n  url: postgres://file/park\nsecret_key: "+key+"\n")
	c, err := Load(path, env(map[string]string{"PARK_DATABASE_URL": "postgres://env/park"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Database.URL != "postgres://env/park" {
		t.Fatalf("database.url = %q, want the environment's value", c.Database.URL)
	}
	if c.HTTP.Addr != "127.0.0.1:9000" {
		t.Fatalf("http.addr = %q, want the file's value", c.HTTP.Addr)
	}
}

// Missing required keys stop startup, and the error names each of them with its variable.
func TestRequiredKeys(t *testing.T) {
	_, err := Load("", env(nil))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"database.url is required (PARK_DATABASE_URL)", "secret_key is required (PARK_SECRET_KEY)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A secret key shorter than MinSecretKeyLength is refused.
func TestShortSecretKey(t *testing.T) {
	_, err := Load("", env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": "short"}))
	if err == nil || !strings.Contains(err.Error(), "secret_key must be at least 32 bytes") {
		t.Fatalf("got %v", err)
	}
}

// A misspelled key in the file is an error, not silently ignored.
func TestUnknownKey(t *testing.T) {
	path := writeFile(t, "database:\n  uri: postgres://file/park\n")
	_, err := Load(path, env(nil))
	if err == nil || !strings.Contains(err.Error(), "field uri not found") {
		t.Fatalf("got %v", err)
	}
}

// An empty file is allowed: the environment may carry everything.
func TestEmptyFile(t *testing.T) {
	path := writeFile(t, "")
	if _, err := Load(path, env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": key})); err != nil {
		t.Fatal(err)
	}
}

// A file that does not exist is an error: a path was given, so it was meant to be read.
func TestMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), env(nil))
	if err == nil || !strings.Contains(err.Error(), "read the configuration file") {
		t.Fatalf("got %v", err)
	}
}

// Park's public address is optional; when set, it is an address with no path, since links are built from it.
func TestPublicURL(t *testing.T) {
	for addr, ok := range map[string]bool{
		"https://park.example.com":      true,
		"https://park.example.com/":     true,
		"http://localhost:8080":         true,
		"park.example.com":              false,
		"https://park.example.com/park": false,
		"https://park.example.com/?x=1": false,
	} {
		_, err := Load("", env(map[string]string{"PARK_DATABASE_URL": "postgres://db/park", "PARK_SECRET_KEY": key,
			"PARK_HTTP_PUBLIC_URL": addr}))
		if ok != (err == nil) {
			t.Errorf("%s: got %v", addr, err)
		}
	}
}
