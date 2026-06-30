// Package daemoncoords reads and writes the flowstate daemon discovery file: a
// 0600 JSON document describing where a running `flowstate serve` daemon can be
// reached and the token required to talk to it. It is intentionally independent
// of the server package so future clients can discover a daemon by importing
// only this package.
package daemoncoords

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/brian-bell/flowstate/internal/artifacts"
)

// Coords describes a discoverable running daemon.
type Coords struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

const (
	coordsEnv  = "FLOWSTATE_DAEMON_COORDS"
	coordsDir  = "flowstate"
	coordsFile = "daemon.json"
)

// Path resolves the coords file location. FLOWSTATE_DAEMON_COORDS overrides the
// default; otherwise the file lives under XDG_RUNTIME_DIR, then XDG_STATE_HOME,
// and finally ~/.local/state. Any environment-provided directory must be
// absolute so discovery never depends on the current working directory.
func Path() (string, error) {
	if override := os.Getenv(coordsEnv); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("%s must be an absolute path: %q", coordsEnv, override)
		}
		return override, nil
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		if !filepath.IsAbs(runtimeDir) {
			return "", fmt.Errorf("XDG_RUNTIME_DIR must be an absolute path: %q", runtimeDir)
		}
		return filepath.Join(runtimeDir, coordsDir, coordsFile), nil
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		if !filepath.IsAbs(stateHome) {
			return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path: %q", stateHome)
		}
		return filepath.Join(stateHome, coordsDir, coordsFile), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve daemon coords path: %w", err)
	}
	return filepath.Join(home, ".local", "state", coordsDir, coordsFile), nil
}

// Write replaces the coords file with c using a 0700 parent directory and a
// 0600 atomic write. It intentionally overwrites any existing coords, including
// stale coords left by a crashed daemon.
func Write(c Coords) error {
	if err := c.validate(); err != nil {
		return err
	}
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	// Secure only directories we create. Tightening permissions on a
	// pre-existing parent we do not own (for example /tmp or XDG_RUNTIME_DIR)
	// is both unnecessary and not permitted; the 0600 file already protects the
	// token.
	existed := isDir(dir)
	if err := os.MkdirAll(dir, artifacts.DirPerm); err != nil {
		return err
	}
	if !existed {
		if err := os.Chmod(dir, artifacts.DirPerm); err != nil {
			return err
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return artifacts.WriteFileAtomic(path, data)
}

// Read loads and validates the coords file. It validates schema and required
// fields only; daemon liveness checks are left to clients because PID reuse and
// platform-specific process probing are out of scope here.
func Read() (Coords, error) {
	path, err := Path()
	if err != nil {
		return Coords{}, err
	}
	return readFrom(path)
}

func readFrom(path string) (Coords, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Coords{}, err
	}
	var c Coords
	if err := json.Unmarshal(data, &c); err != nil {
		return Coords{}, fmt.Errorf("parse daemon coords %q: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return Coords{}, fmt.Errorf("invalid daemon coords %q: %w", path, err)
	}
	return c, nil
}

// RemoveIfMatches deletes the coords file only when it still matches c exactly,
// so one daemon never deletes a newer daemon's discovery file. A missing file
// is not an error; read and remove failures are surfaced.
func RemoveIfMatches(c Coords) error {
	path, err := Path()
	if err != nil {
		return err
	}
	current, err := readFrom(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if current != c {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (c Coords) validate() error {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return fmt.Errorf("url must be an absolute http:// URL with a host: %q", c.URL)
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("token must not be blank")
	}
	if strings.ContainsFunc(c.Token, func(r rune) bool {
		return r <= ' ' || r == 0x7f
	}) {
		return fmt.Errorf("token must not contain whitespace or control characters: %q", c.Token)
	}
	if strings.TrimSpace(c.Version) == "" {
		return errors.New("version must not be blank")
	}
	if c.PID <= 0 {
		return fmt.Errorf("pid must be positive: %d", c.PID)
	}
	return nil
}
