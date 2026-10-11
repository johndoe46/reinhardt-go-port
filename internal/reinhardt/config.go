package reinhardt

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/joho/godotenv"
)

type Config map[string]string

// Config also contains the process environment; only application settings belong
// in diagnostic output.
var configKeys = []string{
	"BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "LISTEN_ADDR", "ALLOWED_HOSTS",
	"URL_PREFIX", "ACCESS_URL_PREFIX", "DELEGATE_DOWNLOAD_PREFIX", "SECRET_KEY",
	"LANGUAGE_CODE", "TIME_ZONE", "SANITIZE_TAGS", "SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS",
	"MUSIC_IMPORT_DIR", "MAX_UPLOAD_BYTES", "COOKIE_SECURE", "LOG_LEVEL", "CSRF_TRUSTED_ORIGINS",
	"JAMA_ENDPOINT", "JAMA_KEY", "JAMA_PROJECT", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "DISCOGS_TOKEN",
	"EMAIL_HOST", "EMAIL_PORT", "EMAIL_USE_TLS", "EMAIL_USE_SSL", "EMAIL_HOST_USER",
	"EMAIL_HOST_PASSWORD", "DEFAULT_FROM_EMAIL",
}

func printConfig(w io.Writer, c Config, showSecrets bool) error {
	keys := append([]string(nil), configKeys...)
	sort.Strings(keys)
	for _, key := range keys {
		value := c[key]
		switch key {
		case "SECRET_KEY", "JAMA_KEY", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "DISCOGS_TOKEN", "EMAIL_HOST_PASSWORD":
			if key == "SECRET_KEY" && value == "" {
				value = "[database-managed; generated on first use if absent]"
			} else if value != "" && !showSecrets {
				value = "[redacted]"
			}
		}
		if _, err := fmt.Fprintf(w, "%s=%q\n", key, value); err != nil {
			return err
		}
	}
	return nil
}

func loadConfig(envFile, database string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return loadConfigFromHome(home, envFile, database)
}

func loadConfigFromHome(home, envFile, database string) (Config, error) {
	userDir := filepath.Join(home, ".reinhardt")
	c := Config{
		"BASE_DIR": userDir, "LISTEN_ADDR": "127.0.0.1:8000",
		"LANGUAGE_CODE": "en-us", "TIME_ZONE": "UTC", "SANITIZE_TAGS": "true",
		"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS": "10800", "EMAIL_HOST": "localhost",
		"EMAIL_PORT": "25", "DEFAULT_FROM_EMAIL": "webmaster@localhost",
		"LOG_LEVEL": "30", "ALLOWED_HOSTS": "127.0.0.1,localhost", "MAX_UPLOAD_BYTES": "536870912",
	}
	// Read the original app's configuration without modifying it. Process values
	// override .env, which overrides the shared configuration.
	for _, file := range []string{filepath.Join(userDir, "config"), envFile} {
		if file == "" {
			continue
		}
		values, e := godotenv.Read(file)
		// The default .env is optional; a misspelled explicit --env-file is an
		// error. Read into the map rather than changing the process environment.
		if os.IsNotExist(e) && (file != envFile || envFile == ".env") {
			continue
		}
		if e != nil {
			return nil, fmt.Errorf("read %s: %w", file, e)
		}
		for k, v := range values {
			c[k] = v
		}
	}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			c[k] = v
		}
	}
	// Path() adds the leading slash, and callers append endpoint names directly.
	// Store nonempty prefixes without a leading slash and with one trailing slash.
	c["URL_PREFIX"] = strings.Trim(c["URL_PREFIX"], "/")
	if c["URL_PREFIX"] != "" {
		c["URL_PREFIX"] += "/"
	}
	if database != "" {
		c["DATABASE_PATH"] = database
	}
	if c["DATABASE_PATH"] == "" {
		c["DATABASE_PATH"] = filepath.Join(c["BASE_DIR"], "db.sqlite3")
	}
	if c["COVERS_DIR"] == "" {
		c["COVERS_DIR"] = filepath.Join(c["BASE_DIR"], "covers")
	}
	if _, e := time.LoadLocation(c["TIME_ZONE"]); e != nil {
		return nil, fmt.Errorf("TIME_ZONE: %w", e)
	}
	return c, nil
}

func (c Config) Bool(k string) bool {
	v := strings.ToLower(c[k])
	return v == "1" || v == "true" || v == "yes" || v == "on"
}
func (c Config) Int(k string) int        { n, _ := strconv.Atoi(c[k]); return n }
func (c Config) Path(path string) string { return "/" + c["URL_PREFIX"] + path }
