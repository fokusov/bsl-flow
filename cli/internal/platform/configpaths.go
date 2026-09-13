package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Environment override keys for the trusted roots. Overrides must name an
// absolute path without symlink components; failures are errors, never a
// silent fallback to the OS default.
const (
	CacheEnvKey  = "BSL_FLOW_CACHE"
	ConfigEnvKey = "BSL_FLOW_CONFIG"
)

// TrustedCacheRoot returns the OS-conventional trusted cache root:
// %LOCALAPPDATA%\BSFlow\cache on windows, ~/Library/Caches/bsl-flow on darwin
// and XDG_CACHE_HOME (or ~/.cache/bsl-flow) elsewhere.
func TrustedCacheRoot() (string, error) {
	deflt, err := defaultCacheRoot(runtime.GOOS, processEnvironment())
	if err != nil {
		return "", err
	}
	return ResolveTrusted(CacheEnvKey, func() string { return deflt })
}

// TrustedConfigRoot returns the OS-conventional trusted config root:
// %LOCALAPPDATA%\BSFlow\config on windows,
// ~/Library/Application Support/bsl-flow on darwin and XDG_CONFIG_HOME (or
// ~/.config/bsl-flow) elsewhere.
func TrustedConfigRoot() (string, error) {
	deflt, err := defaultConfigRoot(runtime.GOOS, processEnvironment())
	if err != nil {
		return "", err
	}
	return ResolveTrusted(ConfigEnvKey, func() string { return deflt })
}

// ResolveTrusted resolves one trusted root: the envKey override when it names
// a non-empty value, otherwise osDefault(). The winning candidate must be an
// absolute path without symlink components; validation failure is an error,
// never a silent fallback.
func ResolveTrusted(envKey string, osDefault func() string) (string, error) {
	candidate := ""
	if envKey != "" {
		candidate = os.Getenv(envKey)
	}
	if candidate == "" {
		candidate = osDefault()
	}
	if candidate == "" {
		return "", errors.New("trusted root resolved to an empty path")
	}
	if !filepath.IsAbs(candidate) {
		return "", fmt.Errorf("trusted root must be an absolute path: %s", candidate)
	}
	canonical, err := NewOSFS().Canonicalize(candidate)
	if err != nil {
		return "", fmt.Errorf("trusted root rejected: %w", err)
	}
	return canonical, nil
}

type environment struct {
	getenv func(string) string
	home   func() (string, error)
}

func processEnvironment() environment {
	return environment{getenv: os.Getenv, home: os.UserHomeDir}
}

func defaultCacheRoot(goos string, env environment) (string, error) {
	switch goos {
	case "windows":
		local := env.getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("LOCALAPPDATA is required for the trusted cache root")
		}
		return filepath.Join(local, "BSLFlow", "cache"), nil
	case "darwin":
		home, err := env.home()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Caches", "bsl-flow"), nil
	default:
		if xdg := env.getenv("XDG_CACHE_HOME"); xdg != "" {
			if !filepath.IsAbs(xdg) {
				return "", fmt.Errorf("XDG_CACHE_HOME must be an absolute path: %s", xdg)
			}
			return filepath.Join(xdg, "bsl-flow"), nil
		}
		home, err := env.home()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".cache", "bsl-flow"), nil
	}
}

func defaultConfigRoot(goos string, env environment) (string, error) {
	switch goos {
	case "windows":
		local := env.getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("LOCALAPPDATA is required for the trusted config root")
		}
		return filepath.Join(local, "BSLFlow", "config"), nil
	case "darwin":
		home, err := env.home()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "bsl-flow"), nil
	default:
		if xdg := env.getenv("XDG_CONFIG_HOME"); xdg != "" {
			if !filepath.IsAbs(xdg) {
				return "", fmt.Errorf("XDG_CONFIG_HOME must be an absolute path: %s", xdg)
			}
			return filepath.Join(xdg, "bsl-flow"), nil
		}
		home, err := env.home()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "bsl-flow"), nil
	}
}
