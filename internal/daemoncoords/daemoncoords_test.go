package daemoncoords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func clearCoordsEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWSTATE_DAEMON_COORDS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
}

func validCoords() Coords {
	return Coords{
		URL:     "http://127.0.0.1:4321",
		Token:   "test-token",
		PID:     4242,
		Version: "flowstate dev (unknown) built unknown",
	}
}

func TestWriteReadRoundTrip0600(t *testing.T) {
	clearCoordsEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", path)

	want := validCoords()
	if err := Write(want); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat coords file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("coords file mode = %v, want 0600", got)
	}

	got, err := Read()
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Read = %+v, want %+v", got, want)
	}
}

func TestWriteCreatesMissingParentAt0700(t *testing.T) {
	clearCoordsEnv(t)
	dir := filepath.Join(t.TempDir(), "flowstate")
	path := filepath.Join(dir, "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", path)

	if err := Write(validCoords()); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat created parent dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("created parent dir mode = %v, want 0700", got)
	}
}

func TestWriteLeavesPreexistingParentModeUntouched(t *testing.T) {
	clearCoordsEnv(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod parent dir: %v", err)
	}
	path := filepath.Join(dir, "daemon.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", path)

	if err := Write(validCoords()); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("pre-existing parent dir mode = %v, want untouched 0755", got)
	}
}

func TestPathUsesEnvOverride(t *testing.T) {
	clearCoordsEnv(t)
	override := filepath.Join(t.TempDir(), "custom", "coords.json")
	t.Setenv("FLOWSTATE_DAEMON_COORDS", override)

	got, err := Path()
	if err != nil {
		t.Fatalf("Path returned error: %v", err)
	}
	if got != override {
		t.Fatalf("Path = %q, want override %q", got, override)
	}
}

func TestPathRejectsRelativeEnvPaths(t *testing.T) {
	tests := []struct {
		name   string
		setEnv func(t *testing.T)
	}{
		{
			name: "override",
			setEnv: func(t *testing.T) {
				t.Setenv("FLOWSTATE_DAEMON_COORDS", "relative/daemon.json")
			},
		},
		{
			name: "runtime dir",
			setEnv: func(t *testing.T) {
				t.Setenv("XDG_RUNTIME_DIR", "relative/run")
			},
		},
		{
			name: "state home",
			setEnv: func(t *testing.T) {
				t.Setenv("XDG_STATE_HOME", "relative/state")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCoordsEnv(t)
			tt.setEnv(t)
			if _, err := Path(); err == nil {
				t.Fatalf("Path returned nil error for relative %s", tt.name)
			}
		})
	}
}

func TestPathDefaultsUnderXDGRuntimeThenState(t *testing.T) {
	runtimeDir := t.TempDir()
	stateHome := t.TempDir()
	home := t.TempDir()

	t.Run("prefers runtime dir", func(t *testing.T) {
		clearCoordsEnv(t)
		t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
		t.Setenv("XDG_STATE_HOME", stateHome)
		got, err := Path()
		if err != nil {
			t.Fatalf("Path returned error: %v", err)
		}
		want := filepath.Join(runtimeDir, "flowstate", "daemon.json")
		if got != want {
			t.Fatalf("Path = %q, want %q", got, want)
		}
	})

	t.Run("falls back to state home", func(t *testing.T) {
		clearCoordsEnv(t)
		t.Setenv("XDG_STATE_HOME", stateHome)
		got, err := Path()
		if err != nil {
			t.Fatalf("Path returned error: %v", err)
		}
		want := filepath.Join(stateHome, "flowstate", "daemon.json")
		if got != want {
			t.Fatalf("Path = %q, want %q", got, want)
		}
	})

	t.Run("falls back to home local state", func(t *testing.T) {
		clearCoordsEnv(t)
		t.Setenv("HOME", home)
		got, err := Path()
		if err != nil {
			t.Fatalf("Path returned error: %v", err)
		}
		want := filepath.Join(home, ".local", "state", "flowstate", "daemon.json")
		if got != want {
			t.Fatalf("Path = %q, want %q", got, want)
		}
	})
}

func TestReadRejectsMalformedOrInvalidFields(t *testing.T) {
	valid := validCoords()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed json", raw: "{not json"},
		{name: "missing url", raw: `{"token":"t","pid":1,"version":"v"}`},
		{name: "missing token", raw: `{"url":"http://127.0.0.1:4321","pid":1,"version":"v"}`},
		{name: "missing version", raw: `{"url":"http://127.0.0.1:4321","token":"t","pid":1}`},
		{name: "non-http url", raw: `{"url":"https://127.0.0.1:4321","token":"t","pid":1,"version":"v"}`},
		{name: "hostless url", raw: `{"url":"http://","token":"t","pid":1,"version":"v"}`},
		{name: "relative url", raw: `{"url":"/healthz","token":"t","pid":1,"version":"v"}`},
		{name: "whitespace token", raw: `{"url":"http://127.0.0.1:4321","token":"to ken","pid":1,"version":"v"}`},
		{name: "control char token", raw: `{"url":"http://127.0.0.1:4321","token":"to\tken","pid":1,"version":"v"}`},
		{name: "blank token", raw: `{"url":"http://127.0.0.1:4321","token":"  ","pid":1,"version":"v"}`},
		{name: "blank version", raw: `{"url":"http://127.0.0.1:4321","token":"t","pid":1,"version":"  "}`},
		{name: "zero pid", raw: `{"url":"http://127.0.0.1:4321","token":"t","pid":0,"version":"v"}`},
		{name: "negative pid", raw: `{"url":"http://127.0.0.1:4321","token":"t","pid":-5,"version":"v"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCoordsEnv(t)
			path := filepath.Join(t.TempDir(), "daemon.json")
			t.Setenv("FLOWSTATE_DAEMON_COORDS", path)
			if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
				t.Fatalf("write coords fixture: %v", err)
			}
			if _, err := Read(); err == nil {
				t.Fatalf("Read returned nil error for %s", tt.name)
			}
		})
	}

	t.Run("valid round-trips", func(t *testing.T) {
		clearCoordsEnv(t)
		path := filepath.Join(t.TempDir(), "daemon.json")
		t.Setenv("FLOWSTATE_DAEMON_COORDS", path)
		data, err := json.Marshal(valid)
		if err != nil {
			t.Fatalf("marshal valid coords: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write valid coords: %v", err)
		}
		got, err := Read()
		if err != nil {
			t.Fatalf("Read returned error for valid coords: %v", err)
		}
		if got != valid {
			t.Fatalf("Read = %+v, want %+v", got, valid)
		}
	})
}

func TestRemoveIfMatchesOnlyRemovesOwnedCoords(t *testing.T) {
	t.Run("missing file is nil", func(t *testing.T) {
		clearCoordsEnv(t)
		path := filepath.Join(t.TempDir(), "daemon.json")
		t.Setenv("FLOWSTATE_DAEMON_COORDS", path)
		if err := RemoveIfMatches(validCoords()); err != nil {
			t.Fatalf("RemoveIfMatches on missing file = %v, want nil", err)
		}
	})

	t.Run("removes exact match", func(t *testing.T) {
		clearCoordsEnv(t)
		path := filepath.Join(t.TempDir(), "daemon.json")
		t.Setenv("FLOWSTATE_DAEMON_COORDS", path)
		owned := validCoords()
		if err := Write(owned); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
		if err := RemoveIfMatches(owned); err != nil {
			t.Fatalf("RemoveIfMatches returned error: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("coords file still present after RemoveIfMatches, stat err = %v", err)
		}
	})

	t.Run("leaves mismatched coords intact", func(t *testing.T) {
		mismatches := map[string]func(Coords) Coords{
			"token":   func(c Coords) Coords { c.Token = "other-token"; return c },
			"url":     func(c Coords) Coords { c.URL = "http://127.0.0.1:9999"; return c },
			"pid":     func(c Coords) Coords { c.PID = c.PID + 1; return c },
			"version": func(c Coords) Coords { c.Version = "other-version"; return c },
		}
		for name, mutate := range mismatches {
			t.Run(name, func(t *testing.T) {
				clearCoordsEnv(t)
				path := filepath.Join(t.TempDir(), "daemon.json")
				t.Setenv("FLOWSTATE_DAEMON_COORDS", path)
				current := validCoords()
				if err := Write(current); err != nil {
					t.Fatalf("Write returned error: %v", err)
				}
				if err := RemoveIfMatches(mutate(current)); err != nil {
					t.Fatalf("RemoveIfMatches returned error: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("coords file removed despite %s mismatch: %v", name, err)
				}
			})
		}
	})
}
