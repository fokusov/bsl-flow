package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixedEnvironment(values map[string]string, home string) environment {
	return environment{
		getenv: func(key string) string { return values[key] },
		home:   func() (string, error) { return home, nil },
	}
}

func TestDefaultTrustedRoots(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		name     string
		goos     string
		values   map[string]string
		home     string
		cache    string
		config   string
		contains string
	}{
		{
			name:   "windows local app data",
			goos:   "windows",
			values: map[string]string{"LOCALAPPDATA": filepath.Join(base, "local")},
			home:   filepath.Join(base, "home"),
			cache:  filepath.Join(base, "local", "BSLFlow", "cache"),
			config: filepath.Join(base, "local", "BSLFlow", "config"),
		},
		{
			name:   "darwin user directories",
			goos:   "darwin",
			values: map[string]string{},
			home:   filepath.Join(base, "mac"),
			cache:  filepath.Join(base, "mac", "Library", "Caches", "bsl-flow"),
			config: filepath.Join(base, "mac", "Library", "Application Support", "bsl-flow"),
		},
		{
			name: "linux xdg",
			goos: "linux",
			values: map[string]string{
				"XDG_CACHE_HOME":  filepath.Join(base, "xdg-cache"),
				"XDG_CONFIG_HOME": filepath.Join(base, "xdg-config"),
			},
			home:   filepath.Join(base, "penguin"),
			cache:  filepath.Join(base, "xdg-cache", "bsl-flow"),
			config: filepath.Join(base, "xdg-config", "bsl-flow"),
		},
		{
			name:   "linux home fallback",
			goos:   "linux",
			values: map[string]string{},
			home:   filepath.Join(base, "penguin"),
			cache:  filepath.Join(base, "penguin", ".cache", "bsl-flow"),
			config: filepath.Join(base, "penguin", ".config", "bsl-flow"),
		},
		{
			name:     "windows missing localappdata",
			goos:     "windows",
			values:   map[string]string{},
			contains: "LOCALAPPDATA",
		},
		{
			name: "linux relative xdg",
			goos: "linux",
			values: map[string]string{
				"XDG_CACHE_HOME":  "relative/cache",
				"XDG_CONFIG_HOME": "relative/config",
			},
			contains: "XDG_",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			env := fixedEnvironment(item.values, item.home)
			cache, cacheErr := defaultCacheRoot(item.goos, env)
			config, configErr := defaultConfigRoot(item.goos, env)
			if item.contains != "" {
				if cacheErr == nil || !strings.Contains(cacheErr.Error(), item.contains) {
					t.Fatalf("cache error = %v, want %q", cacheErr, item.contains)
				}
				if configErr == nil || !strings.Contains(configErr.Error(), item.contains) {
					t.Fatalf("config error = %v, want %q", configErr, item.contains)
				}
				return
			}
			if cacheErr != nil {
				t.Fatal(cacheErr)
			}
			if configErr != nil {
				t.Fatal(configErr)
			}
			if cache != item.cache {
				t.Fatalf("cache = %q, want %q", cache, item.cache)
			}
			if config != item.config {
				t.Fatalf("config = %q, want %q", config, item.config)
			}
		})
	}
}

func TestDefaultTrustedRootRequiresHome(t *testing.T) {
	env := environment{
		getenv: func(string) string { return "" },
		home:   func() (string, error) { return "", os.ErrNotExist },
	}
	for _, goos := range []string{"darwin", "linux"} {
		if _, err := defaultCacheRoot(goos, env); err == nil {
			t.Fatalf("cache root accepted a missing home on %s", goos)
		}
		if _, err := defaultConfigRoot(goos, env); err == nil {
			t.Fatalf("config root accepted a missing home on %s", goos)
		}
	}
}

func TestResolveTrustedEnvironmentOverrideAndFallback(t *testing.T) {
	base := t.TempDir()
	override := filepath.Join(base, "override")
	if err := os.Mkdir(override, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BSL_FLOW_TEST_ROOT", override)
	root, err := ResolveTrusted("BSL_FLOW_TEST_ROOT", func() string { return filepath.Join(base, "default") })
	if err != nil {
		t.Fatal(err)
	}
	if root != override {
		t.Fatalf("root = %q, want override %q", root, override)
	}

	t.Setenv("BSL_FLOW_TEST_ROOT", "")
	root, err = ResolveTrusted("BSL_FLOW_TEST_ROOT", func() string { return filepath.Join(base, "default") })
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(base, "default") {
		t.Fatalf("root = %q, want default %q", root, filepath.Join(base, "default"))
	}
}

func TestResolveTrustedRejectsRelativeOverride(t *testing.T) {
	t.Setenv("BSL_FLOW_TEST_ROOT", "relative/root")
	root, err := ResolveTrusted("BSL_FLOW_TEST_ROOT", func() string { return t.TempDir() })
	if err == nil {
		t.Fatalf("relative override accepted as %q", root)
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error is not explicit: %v", err)
	}
}

func TestResolveTrustedRejectsSymlinkedOverride(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable on this host: %v", err)
	}
	t.Setenv("BSL_FLOW_TEST_ROOT", link)
	root, err := ResolveTrusted("BSL_FLOW_TEST_ROOT", func() string { return real })
	if err == nil {
		t.Fatalf("symlinked override accepted as %q", root)
	}
	if root != "" {
		t.Fatalf("rejected override returned path %q", root)
	}
}

func TestResolveTrustedRejectsEmptyDefault(t *testing.T) {
	if root, err := ResolveTrusted("BSL_FLOW_TEST_ROOT", func() string { return "" }); err == nil {
		t.Fatalf("empty default accepted as %q", root)
	}
}
