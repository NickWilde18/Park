// Package config loads Park's server configuration from a file and from environment variables.
//
// The file is YAML. Every key can also be set by an environment variable, named PARK_ followed by the key in upper
// case with dots turned into underscores: database.url is PARK_DATABASE_URL. The environment wins over the file.
// Unknown keys in the file are errors, so that a misspelled key fails at startup instead of being ignored.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

// MinSecretKeyLength is the shortest secret key accepted, in bytes.
const MinSecretKeyLength = 32

// Config is Park's server configuration.
type Config struct {
	HTTP struct {
		// Addr is the address the HTTP server listens on.
		Addr string `yaml:"addr"`
	} `yaml:"http"`
	Database struct {
		// URL is the Postgres connection string.
		URL string `yaml:"url"`
	} `yaml:"database"`
	// SecretKey encrypts the secrets Park stores, such as forge credentials.
	SecretKey string `yaml:"secret_key"`
}

// Load reads the file at path, if path is not empty, applies the environment through getenv, fills in defaults,
// and validates the result. The error names every key that is missing or invalid.
func Load(path string, getenv func(string) string) (*Config, error) {
	var c Config
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read the configuration file: %w", err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse the configuration file %s: %w", path, err)
		}
	}
	for _, v := range c.variables() {
		if s := getenv(v.name); s != "" {
			*v.value = s
		}
	}
	if c.HTTP.Addr == "" {
		c.HTTP.Addr = ":8080"
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

type variable struct {
	name  string
	value *string
}

// variables pairs every key with its environment variable.
func (c *Config) variables() []variable {
	return []variable{
		{"PARK_HTTP_ADDR", &c.HTTP.Addr},
		{"PARK_DATABASE_URL", &c.Database.URL},
		{"PARK_SECRET_KEY", &c.SecretKey},
	}
}

func (c *Config) validate() error {
	var errs []error
	if c.Database.URL == "" {
		errs = append(errs, errors.New("database.url is required (PARK_DATABASE_URL)"))
	}
	switch {
	case c.SecretKey == "":
		errs = append(errs, errors.New("secret_key is required (PARK_SECRET_KEY)"))
	case len(c.SecretKey) < MinSecretKeyLength:
		errs = append(errs, fmt.Errorf("secret_key must be at least %d bytes long", MinSecretKeyLength))
	}
	return errors.Join(errs...)
}
