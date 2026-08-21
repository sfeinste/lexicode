// Package config loads the Lexicode runtime configuration.
//
// Precedence, highest first (architecture §14):
//
//  1. command-line flags that were explicitly set
//  2. LEXICODE_* environment variables
//  3. ~/.lexicode/config.yaml
//  4. built-in defaults
//
// Only the handful of settings a process needs before it can open its database live here.
// Everything else is stored in the database and edited in the UI.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultPort is the port the server listens on unless told otherwise.
const DefaultPort = 7717

// FileName is the base name of the configuration file inside the config directory.
const FileName = "config.yaml"

// EnvPrefix is the prefix of every environment variable this package reads.
const EnvPrefix = "LEXICODE_"

// Config is the fully resolved configuration.
type Config struct {
	// Host is the interface the HTTP server binds to.
	Host string `yaml:"host"`
	// Port is the TCP port the HTTP server binds to.
	Port int `yaml:"port"`
	// DataDir is where the database, logs, master key and workspaces live.
	DataDir string `yaml:"data_dir"`
	// DockerHost overrides the Docker endpoint. Empty means "use the environment".
	DockerHost string `yaml:"docker_host"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `yaml:"log_level"`
	// OpenBrowser opens a browser tab at the dashboard when the server is ready.
	OpenBrowser bool `yaml:"open_browser"`

	// source records where the loader looked for a config file, for diagnostics.
	source string
}

// Addr is the host:port the HTTP server listens on.
func (c Config) Addr() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// URL is the address a browser should open.
func (c Config) URL() string {
	host := c.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d/", host, c.Port)
}

// FilePath is the config file the loader read, or would have read. It may not exist.
func (c Config) FilePath() string { return c.source }

// LogFile is the JSON log file inside the data directory.
func (c Config) LogFile() string { return filepath.Join(c.DataDir, "logs", "lexicode.log") }

// Defaults returns the built-in configuration, before any file, environment or flag is consulted.
// home is the user's home directory; when empty the data directory falls back to ".lexicode"
// relative to the working directory.
func Defaults(home string) Config {
	dataDir := ".lexicode"
	if home != "" {
		dataDir = filepath.Join(home, ".lexicode")
	}
	return Config{
		Host:        "127.0.0.1",
		Port:        DefaultPort,
		DataDir:     dataDir,
		DockerHost:  "",
		LogLevel:    "info",
		OpenBrowser: true,
	}
}

// Options controls a Load. The zero value reads the real environment, the real home directory and
// consults no flags.
type Options struct {
	// Home overrides the user's home directory. Empty means os.UserHomeDir.
	Home string
	// File overrides the config file path. Empty means <home>/.lexicode/config.yaml, or the value
	// of LEXICODE_CONFIG if that is set.
	File string
	// Lookup reads an environment variable. Empty means os.LookupEnv.
	Lookup func(string) (string, bool)
	// Flags, when non-nil, must already be parsed. Only flags that were explicitly set on the
	// command line are applied, which is what makes flags outrank the environment without a
	// default value silently outranking it too.
	Flags *flag.FlagSet
}

// Bind registers the configuration flags on fs and returns fs. The registered defaults are
// deliberately zero values: Load only reads flags the user actually set.
func Bind(fs *flag.FlagSet) *flag.FlagSet {
	fs.String("host", "", "interface to bind (default 127.0.0.1)")
	fs.Int("port", 0, "port to bind (default 7717)")
	fs.String("data-dir", "", "directory for the database, logs and workspaces (default ~/.lexicode)")
	fs.String("docker-host", "", "Docker endpoint override (default from the environment)")
	fs.String("log-level", "", "debug, info, warn or error (default info)")
	fs.Bool("open-browser", false, "open a browser tab when the server is ready (default true)")
	fs.String("config", "", "path to config.yaml (default ~/.lexicode/config.yaml)")
	return fs
}

// yamlConfig mirrors Config with pointers so that an absent key leaves the default in place
// instead of overwriting it with a zero value.
type yamlConfig struct {
	Host        *string `yaml:"host"`
	Port        *int    `yaml:"port"`
	DataDir     *string `yaml:"data_dir"`
	DockerHost  *string `yaml:"docker_host"`
	LogLevel    *string `yaml:"log_level"`
	OpenBrowser *bool   `yaml:"open_browser"`
}

// Load resolves the configuration. A missing config file is not an error; an unreadable or
// malformed one is.
func Load(opts Options) (Config, error) {
	lookup := opts.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}

	home := opts.Home
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}

	cfg := Defaults(home)

	path := opts.File
	if path == "" {
		if v, ok := lookup(EnvPrefix + "CONFIG"); ok && v != "" {
			path = v
		}
	}
	if path == "" && opts.Flags != nil {
		if v, ok := setFlag(opts.Flags, "config"); ok && v != "" {
			path = v
		}
	}
	if path == "" {
		path = filepath.Join(Defaults(home).DataDir, FileName)
	}
	cfg.source = path

	if err := applyFile(&cfg, path); err != nil {
		return Config{}, err
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		return Config{}, err
	}
	if err := applyFlags(&cfg, opts.Flags); err != nil {
		return Config{}, err
	}

	cfg.DataDir = expandHome(cfg.DataDir, home)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyFile(cfg *Config, path string) error {
	f, err := os.Open(path) //nolint:gosec // the path is operator-supplied by design
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var raw yamlConfig
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}

	assign(&cfg.Host, raw.Host)
	assign(&cfg.Port, raw.Port)
	assign(&cfg.DataDir, raw.DataDir)
	assign(&cfg.DockerHost, raw.DockerHost)
	assign(&cfg.LogLevel, raw.LogLevel)
	assign(&cfg.OpenBrowser, raw.OpenBrowser)
	return nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	str := func(name string, dst *string) {
		if v, ok := lookup(EnvPrefix + name); ok {
			*dst = v
		}
	}
	str("HOST", &cfg.Host)
	str("DATA_DIR", &cfg.DataDir)
	str("DOCKER_HOST", &cfg.DockerHost)
	str("LOG_LEVEL", &cfg.LogLevel)

	if v, ok := lookup(EnvPrefix + "PORT"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("%sPORT=%q is not a number; set it to a port between 1 and 65535", EnvPrefix, v)
		}
		cfg.Port = n
	}
	if v, ok := lookup(EnvPrefix + "OPEN_BROWSER"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("%sOPEN_BROWSER=%q is not a boolean; set it to true or false", EnvPrefix, v)
		}
		cfg.OpenBrowser = b
	}
	return nil
}

func applyFlags(cfg *Config, fs *flag.FlagSet) error {
	if fs == nil {
		return nil
	}
	if v, ok := setFlag(fs, "host"); ok {
		cfg.Host = v
	}
	if v, ok := setFlag(fs, "data-dir"); ok {
		cfg.DataDir = v
	}
	if v, ok := setFlag(fs, "docker-host"); ok {
		cfg.DockerHost = v
	}
	if v, ok := setFlag(fs, "log-level"); ok {
		cfg.LogLevel = v
	}
	if v, ok := setFlag(fs, "port"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("--port=%q is not a number; set it to a port between 1 and 65535", v)
		}
		cfg.Port = n
	}
	if v, ok := setFlag(fs, "open-browser"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("--open-browser=%q is not a boolean; set it to true or false", v)
		}
		cfg.OpenBrowser = b
	}
	return nil
}

// setFlag reports the string form of a flag only if it was explicitly set on the command line.
func setFlag(fs *flag.FlagSet, name string) (string, bool) {
	var value string
	var found bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			value = f.Value.String()
			found = true
		}
	})
	return value, found
}

func assign[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func expandHome(path, home string) string {
	if home == "" {
		return path
	}
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, path[2:])
	default:
		return path
	}
}

var validLevels = []string{"debug", "info", "warn", "error"}

// Validate reports the first setting that would stop the server from starting, naming the setting
// and the value that is wrong.
func (c Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d is out of range; set port to a value between 1 and 65535", c.Port)
	}
	if c.DataDir == "" {
		return errors.New("data_dir is empty; set data_dir to a writable directory such as ~/.lexicode")
	}
	if !slices.Contains(validLevels, c.LogLevel) {
		return fmt.Errorf("log_level %q is not recognised; use one of %s", c.LogLevel, strings.Join(validLevels, ", "))
	}
	return nil
}
