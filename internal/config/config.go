// Package config reads the server configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration. Load fills it from the
// environment; everything has a default that works inside the Oakestra
// root orchestrator's compose network.
type Config struct {
	// OakestraAPIURL is the system manager base URL. Tokens are verified and
	// refreshed there, and all application/service calls go there.
	OakestraAPIURL string
	// RootRAURL is the root resource abstractor, which holds one candidate
	// per cluster.
	RootRAURL string
	// ClusterRAPort is the host port each cluster orchestrator publishes its
	// resource abstractor on. The host is the cluster's IP as reported by
	// the root resource abstractor.
	ClusterRAPort int
	ListenAddr    string
	DataDir       string
	// PublicURL is the MCP URL agents should use, as reachable from outside.
	// It is only displayed, so an empty value is fine.
	PublicURL         string
	EnableWrites      bool
	ConfirmTTL        time.Duration
	ActivityRetention time.Duration
}

// Defaults returns the configuration used when no variables are set.
func Defaults() Config {
	return Config{
		OakestraAPIURL:    "http://system_manager:10000",
		RootRAURL:         "http://root_resource_abstractor:11011",
		ClusterRAPort:     11012,
		ListenAddr:        ":8090",
		DataDir:           "/data",
		ConfirmTTL:        5 * time.Minute,
		ActivityRetention: 720 * time.Hour,
	}
}

// Load reads the configuration from the process environment.
func Load() (Config, error) {
	return FromEnv(os.Getenv)
}

// FromEnv reads the configuration through getenv, which makes it testable
// without touching the real environment. All problems are reported at once
// so a misconfigured deployment can be fixed in one go.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Defaults()
	var errs []error

	str := func(key string, dst *string) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			*dst = v
		}
	}
	str("OAKESTRA_API_URL", &c.OakestraAPIURL)
	str("ROOT_RA_URL", &c.RootRAURL)
	str("LISTEN_ADDR", &c.ListenAddr)
	str("DATA_DIR", &c.DataDir)
	str("PUBLIC_URL", &c.PublicURL)

	if v := strings.TrimSpace(getenv("CLUSTER_RA_PORT")); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			errs = append(errs, fmt.Errorf("CLUSTER_RA_PORT: %q is not a valid port", v))
		} else {
			c.ClusterRAPort = p
		}
	}
	if v := strings.TrimSpace(getenv("MCP_ENABLE_WRITES")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("MCP_ENABLE_WRITES: %q is not a boolean", v))
		} else {
			c.EnableWrites = b
		}
	}
	dur := func(key string, dst *time.Duration) {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: %q is not a positive duration (e.g. 5m, 720h)", key, v))
			return
		}
		*dst = d
	}
	dur("CONFIRM_TTL", &c.ConfirmTTL)
	dur("ACTIVITY_RETENTION", &c.ActivityRetention)

	errs = append(errs, c.validate()...)
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	c.OakestraAPIURL = strings.TrimRight(c.OakestraAPIURL, "/")
	c.RootRAURL = strings.TrimRight(c.RootRAURL, "/")
	return c, nil
}

func (c Config) validate() []error {
	var errs []error
	for _, u := range []struct{ key, val string }{
		{"OAKESTRA_API_URL", c.OakestraAPIURL},
		{"ROOT_RA_URL", c.RootRAURL},
	} {
		if err := checkHTTPURL(u.val); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u.key, err))
		}
	}
	if c.PublicURL != "" {
		if err := checkHTTPURL(c.PublicURL); err != nil {
			errs = append(errs, fmt.Errorf("PUBLIC_URL: %w", err))
		}
	}
	if c.ListenAddr == "" || !strings.Contains(c.ListenAddr, ":") {
		errs = append(errs, fmt.Errorf("LISTEN_ADDR: %q must be host:port or :port", c.ListenAddr))
	}
	return errs
}

func checkHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an absolute http(s) URL", raw)
	}
	return nil
}
