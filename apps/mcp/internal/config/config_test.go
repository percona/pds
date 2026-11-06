package config_test

import (
	"strings"
	"testing"

	"github.com/percona/pds/apps/mcp/internal/config"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestTheControlPlaneDefaultsToPercona(t *testing.T) {
	cfg, err := config.Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Origin != "https://pds.percona.com" || cfg.MCPURL != "https://pds.percona.com/mcp" {
		t.Errorf("Origin, MCPURL = %q, %q", cfg.Origin, cfg.MCPURL)
	}
}

func TestEachRemovedSettingStopsTheBinaryAndNamesTheLoginCommand(t *testing.T) {
	removed := []string{
		"PDS_USERNAME", "PDS_PASSWORD", "PDS_OPERATOR_TOKEN", "PDS_ORGANIZATION", "PDS_TEAM",
		"PDS_ALLOW_WRITES", "PDS_ALLOW_DESTRUCTIVE",
	}
	for _, name := range removed {
		const sentinel = "sentinel-value-1f3a"
		_, err := config.Load(env(map[string]string{name: sentinel}))
		if err == nil {
			t.Fatalf("%s did not stop the binary", name)
		}
		if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "pds-mcp login") {
			t.Errorf("%s: message %q does not name the setting and pds-mcp login", name, err)
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("%s: message repeats the value", name)
		}
	}
}

func TestEverySetRemovedSettingIsNamedOnce(t *testing.T) {
	_, err := config.Load(env(map[string]string{"PDS_USERNAME": "a", "PDS_PASSWORD": "b"}))
	if err == nil || !strings.Contains(err.Error(), "PDS_USERNAME") || !strings.Contains(err.Error(), "PDS_PASSWORD") {
		t.Fatalf("err = %v, want both settings named", err)
	}
}

func TestAnEmptyRemovedSettingIsIgnored(t *testing.T) {
	if _, err := config.Load(env(map[string]string{"PDS_PASSWORD": ""})); err != nil {
		t.Fatalf("an empty setting stopped the binary: %v", err)
	}
}

func TestControlPlaneAddresses(t *testing.T) {
	cases := []struct {
		url, origin string
		ok          bool
	}{
		{"https://pds.example.com", "https://pds.example.com", true},
		{"https://PDS.Example.com/", "https://pds.example.com", true},
		{"https://pds.example.com:8443", "https://pds.example.com:8443", true},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://localhost:8080", "http://localhost:8080", true},
		{"http://[::1]:8080", "http://[::1]:8080", true},
		{"http://pds.example.com", "", false},
		{"http://host.docker.internal:18080", "", false},
		{"http://127.0.0.1.example.com", "", false},
		{"ftp://pds.example.com", "", false},
		{"https://user:pass@pds.example.com", "", false},
		{"https://pds.example.com/base", "", false},
		{"https://pds.example.com/?a=b", "", false},
		{"https://pds.example.com/#frag", "", false},
		{"not a url", "", false},
	}
	for _, c := range cases {
		cfg, err := config.Load(env(map[string]string{"PDS_URL": c.url}))
		if (err == nil) != c.ok {
			t.Errorf("PDS_URL=%q: err = %v, want ok=%v", c.url, err, c.ok)
			continue
		}
		if c.ok && cfg.Origin != c.origin {
			t.Errorf("PDS_URL=%q: origin = %q, want %q", c.url, cfg.Origin, c.origin)
		}
		if err != nil && strings.Contains(err.Error(), "pass@") {
			t.Errorf("PDS_URL=%q: the message repeats the address: %v", c.url, err)
		}
	}
}

func TestTheLoginFileLivesUnderTheConfigDirectory(t *testing.T) {
	got, err := config.LoginFile(env(map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "HOME": "/h"}))
	if err != nil || got != "/x/cfg/pds-mcp/credentials.json" {
		t.Errorf("LoginFile = %q, %v", got, err)
	}
	got, err = config.LoginFile(env(map[string]string{"HOME": "/h"}))
	if err != nil || got != "/h/.config/pds-mcp/credentials.json" {
		t.Errorf("LoginFile = %q, %v", got, err)
	}
	if _, err = config.LoginFile(env(nil)); err == nil {
		t.Error("no home directory and no XDG_CONFIG_HOME did not fail")
	}
}

func TestTheConnectionsFileLivesBesideTheLoginFile(t *testing.T) {
	got, err := config.ConnectionsFile(env(map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "HOME": "/h"}))
	if err != nil || got != "/x/cfg/pds-mcp/connections.json" {
		t.Errorf("ConnectionsFile = %q, %v", got, err)
	}
	if _, err := config.ConnectionsFile(env(nil)); err == nil {
		t.Error("no home directory was reported")
	}
}
