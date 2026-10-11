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

func printConfig(writer io.Writer, config Config, showSecrets bool) error {
	keys := append([]string(nil), configKeys...)
	sort.Strings(keys)
	for _, key := range keys {
		value := config[key]
		switch key {
		case "SECRET_KEY", "JAMA_KEY", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "DISCOGS_TOKEN", "EMAIL_HOST_PASSWORD":
			if key == "SECRET_KEY" && value == "" {
				value = "[database-managed; generated on first use if absent]"
			} else if value != "" && !showSecrets {
				value = "[redacted]"
			}
		}
		if _, err := fmt.Fprintf(writer, "%s=%q\n", key, value); err != nil {
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
	config := Config{
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
		values, err := godotenv.Read(file)
		// The default .env is optional; a misspelled explicit --env-file is an
		// error. Read into the map rather than changing the process environment.
		if os.IsNotExist(err) && (file != envFile || envFile == ".env") {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		for key, value := range values {
			config[key] = value
		}
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			config[key] = value
		}
	}
	// Path() adds the leading slash, and callers append endpoint names directly.
	// Store nonempty prefixes without a leading slash and with one trailing slash.
	config["URL_PREFIX"] = strings.Trim(config["URL_PREFIX"], "/")
	if config["URL_PREFIX"] != "" {
		config["URL_PREFIX"] += "/"
	}
	if database != "" {
		config["DATABASE_PATH"] = database
	}
	if config["DATABASE_PATH"] == "" {
		config["DATABASE_PATH"] = filepath.Join(config["BASE_DIR"], "db.sqlite3")
	}
	if config["COVERS_DIR"] == "" {
		config["COVERS_DIR"] = filepath.Join(config["BASE_DIR"], "covers")
	}
	if _, err := time.LoadLocation(config["TIME_ZONE"]); err != nil {
		return nil, fmt.Errorf("TIME_ZONE: %w", err)
	}
	return config, nil
}

func (config Config) Bool(key string) bool {
	value := strings.ToLower(config[key])
	return value == "1" || value == "true" || value == "yes" || value == "on"
}
func (config Config) Int(key string) int {
	integerValue, _ := strconv.Atoi(config[key])
	return integerValue
}
func (config Config) Path(path string) string { return "/" + config["URL_PREFIX"] + path }
