package config_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spruce/lexicode/internal/config"
)

// env builds a lookup function over a fixed map, so tests never touch the real environment.
func env(pairs map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := pairs[k]
		return v, ok
	}
}

func parsedFlags(t *testing.T, args ...string) *flag.FlagSet {
	t.Helper()
	fs := config.Bind(flag.NewFlagSet("test", flag.ContinueOnError))
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	return fs
}

func writeConfig(t *testing.T, body string) (home, path string) {
	t.Helper()
	home = t.TempDir()
	dir := filepath.Join(home, ".lexicode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func TestDefaultsWhenNothingIsSet(t *testing.T) {
	home := t.TempDir()

	got, err := config.Load(config.Options{Home: home, Lookup: env(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := config.Defaults(home)
	want.Host = "127.0.0.1"
	if got.Host != want.Host {
		t.Errorf("host = %q, want %q", got.Host, want.Host)
	}
	if got.Port != config.DefaultPort {
		t.Errorf("port = %d, want %d", got.Port, config.DefaultPort)
	}
	if got.DataDir != filepath.Join(home, ".lexicode") {
		t.Errorf("data_dir = %q, want %q", got.DataDir, filepath.Join(home, ".lexicode"))
	}
	if got.LogLevel != "info" {
		t.Errorf("log_level = %q, want %q", got.LogLevel, "info")
	}
	if !got.OpenBrowser {
		t.Error("open_browser = false, want true")
	}
	if got.DockerHost != "" {
		t.Errorf("docker_host = %q, want empty", got.DockerHost)
	}
}

// TestPrecedence is the story S01 acceptance test for configuration: flags beat environment beats
// file beats defaults, checked one layer at a time on a single field plus a field per layer that
// nothing else touches.
func TestPrecedence(t *testing.T) {
	fileBody := "host: 10.0.0.1\nport: 1111\nlog_level: warn\ndocker_host: unix:///file.sock\n"

	t.Run("file beats defaults", func(t *testing.T) {
		home, _ := writeConfig(t, fileBody)
		got, err := config.Load(config.Options{Home: home, Lookup: env(nil)})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 1111 {
			t.Errorf("port = %d, want 1111", got.Port)
		}
		if got.Host != "10.0.0.1" {
			t.Errorf("host = %q, want 10.0.0.1", got.Host)
		}
		if got.LogLevel != "warn" {
			t.Errorf("log_level = %q, want warn", got.LogLevel)
		}
	})

	t.Run("env beats file", func(t *testing.T) {
		home, _ := writeConfig(t, fileBody)
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(map[string]string{"LEXICODE_PORT": "2222", "LEXICODE_OPEN_BROWSER": "false"}),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 2222 {
			t.Errorf("port = %d, want 2222 (env must beat the file)", got.Port)
		}
		if got.OpenBrowser {
			t.Error("open_browser = true, want false from the environment")
		}
		if got.Host != "10.0.0.1" {
			t.Errorf("host = %q, want 10.0.0.1 (untouched by env, must still come from the file)", got.Host)
		}
		if got.DockerHost != "unix:///file.sock" {
			t.Errorf("docker_host = %q, want the file value", got.DockerHost)
		}
	})

	t.Run("flags beat env", func(t *testing.T) {
		home, _ := writeConfig(t, fileBody)
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(map[string]string{"LEXICODE_PORT": "2222", "LEXICODE_LOG_LEVEL": "error"}),
			Flags:  parsedFlags(t, "--port", "3333", "--host", "0.0.0.0"),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 3333 {
			t.Errorf("port = %d, want 3333 (flag must beat env)", got.Port)
		}
		if got.Host != "0.0.0.0" {
			t.Errorf("host = %q, want 0.0.0.0", got.Host)
		}
		if got.LogLevel != "error" {
			t.Errorf("log_level = %q, want error (env, since no flag was set)", got.LogLevel)
		}
	})

	t.Run("unset flags do not override anything", func(t *testing.T) {
		home, _ := writeConfig(t, fileBody)
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(nil),
			// Every flag is registered but none is set on the command line.
			Flags: parsedFlags(t),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 1111 {
			t.Errorf("port = %d, want 1111; a registered-but-unset flag must not win", got.Port)
		}
		if !got.OpenBrowser {
			t.Error("open_browser = false; the false default of an unset bool flag must not win")
		}
	})

	t.Run("explicit false bool flag wins", func(t *testing.T) {
		home, _ := writeConfig(t, "open_browser: true\n")
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(map[string]string{"LEXICODE_OPEN_BROWSER": "true"}),
			Flags:  parsedFlags(t, "--open-browser=false"),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.OpenBrowser {
			t.Error("open_browser = true, want false from --open-browser=false")
		}
	})
}

func TestConfigFileLocationOverrides(t *testing.T) {
	home := t.TempDir()
	custom := filepath.Join(home, "elsewhere.yaml")
	if err := os.WriteFile(custom, []byte("port: 4444\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("LEXICODE_CONFIG", func(t *testing.T) {
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(map[string]string{"LEXICODE_CONFIG": custom}),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 4444 {
			t.Errorf("port = %d, want 4444", got.Port)
		}
	})

	t.Run("--config flag", func(t *testing.T) {
		got, err := config.Load(config.Options{
			Home:   home,
			Lookup: env(nil),
			Flags:  parsedFlags(t, "--config", custom),
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.Port != 4444 {
			t.Errorf("port = %d, want 4444", got.Port)
		}
		if got.FilePath() != custom {
			t.Errorf("FilePath() = %q, want %q", got.FilePath(), custom)
		}
	})
}

func TestMissingFileIsNotAnError(t *testing.T) {
	home := t.TempDir()
	got, err := config.Load(config.Options{Home: home, Lookup: env(nil)})
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}
	if got.Port != config.DefaultPort {
		t.Errorf("port = %d, want the default %d", got.Port, config.DefaultPort)
	}
}

func TestTildeInDataDirExpands(t *testing.T) {
	home, _ := writeConfig(t, "data_dir: ~/somewhere\n")
	got, err := config.Load(config.Options{Home: home, Lookup: env(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, "somewhere"); got.DataDir != want {
		t.Errorf("data_dir = %q, want %q", got.DataDir, want)
	}
}

func TestErrorsNameTheOffendingSetting(t *testing.T) {
	tests := []struct {
		name    string
		opts    func(home string) config.Options
		file    string
		wantSub string
	}{
		{
			name:    "unknown key in the file",
			file:    "prot: 9\n",
			opts:    func(home string) config.Options { return config.Options{Home: home, Lookup: env(nil)} },
			wantSub: "prot",
		},
		{
			name: "non-numeric port in the environment",
			file: "",
			opts: func(home string) config.Options {
				return config.Options{Home: home, Lookup: env(map[string]string{"LEXICODE_PORT": "http"})}
			},
			wantSub: "LEXICODE_PORT",
		},
		{
			name:    "port out of range",
			file:    "port: 70000\n",
			opts:    func(home string) config.Options { return config.Options{Home: home, Lookup: env(nil)} },
			wantSub: "70000",
		},
		{
			name:    "unknown log level",
			file:    "log_level: chatty\n",
			opts:    func(home string) config.Options { return config.Options{Home: home, Lookup: env(nil)} },
			wantSub: "chatty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := writeConfig(t, tc.file)
			_, err := config.Load(tc.opts(home))
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestAddrAndURL(t *testing.T) {
	cfg := config.Config{Host: "0.0.0.0", Port: 7717}
	if cfg.Addr() != "0.0.0.0:7717" {
		t.Errorf("Addr() = %q", cfg.Addr())
	}
	if cfg.URL() != "http://localhost:7717/" {
		t.Errorf("URL() = %q, want the browser-usable localhost form", cfg.URL())
	}
}
