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

// Config contains effective settings, parsed and validated before opening the
// database or starting HTTP and background jobs.
type Config struct {
	BaseDir                          string
	DatabasePath                     string
	CoversDir                        string
	ListenAddr                       string
	AllowedHosts                     []string
	URLPrefix                        string
	AccessURLPrefix                  string
	DelegateDownloadPrefix           string
	SecretKey                        string
	LanguageCode                     string
	TimeZone                         string
	SanitizeTags                     bool
	SharedPlaylistDownloadMaxSeconds int64
	MusicImportDir                   string
	MaxUploadBytes                   int64
	CookieSecure                     bool
	LogLevel                         int
	CSRFTrustedOrigins               []string
	LastFMAPIKey                     string
	JamaEndpoint                     string
	JamaKey                          string
	JamaProject                      int64
	DropboxKey                       string
	DropboxSecret                    string
	DropboxToken                     string
	EmailHost                        string
	EmailPort                        int
	EmailUseTLS                      bool
	EmailUseSSL                      bool
	EmailHostUser                    string
	EmailHostPassword                string
	DefaultFromEmail                 string
}

const defaultLastFMAPIKey = "6dd57d914fac3c0f2107cad48833db39"

var configKeys = []string{
	"BASE_DIR",
	"DATABASE_PATH",
	"COVERS_DIR",
	"LISTEN_ADDR",
	"ALLOWED_HOSTS",
	"URL_PREFIX",
	"ACCESS_URL_PREFIX",
	"DELEGATE_DOWNLOAD_PREFIX",
	"SECRET_KEY",
	"LANGUAGE_CODE",
	"TIME_ZONE",
	"SANITIZE_TAGS",
	"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS",
	"MUSIC_IMPORT_DIR",
	"MAX_UPLOAD_BYTES",
	"COOKIE_SECURE",
	"LOG_LEVEL",
	"CSRF_TRUSTED_ORIGINS",
	"LASTFM_API_KEY",
	"JAMA_ENDPOINT",
	"JAMA_KEY",
	"JAMA_PROJECT",
	"DBX_KEY",
	"DBX_SECRET",
	"DBX_TOKEN",
	"EMAIL_HOST",
	"EMAIL_PORT",
	"EMAIL_USE_TLS",
	"EMAIL_USE_SSL",
	"EMAIL_HOST_USER",
	"EMAIL_HOST_PASSWORD",
	"DEFAULT_FROM_EMAIL",
}

// values serializes only application settings for the config command.
func (config *Config) values() map[string]string {
	values := map[string]string{
		"BASE_DIR":                             config.BaseDir,
		"DATABASE_PATH":                        config.DatabasePath,
		"COVERS_DIR":                           config.CoversDir,
		"LISTEN_ADDR":                          config.ListenAddr,
		"ALLOWED_HOSTS":                        strings.Join(config.AllowedHosts, ","),
		"URL_PREFIX":                           config.URLPrefix,
		"ACCESS_URL_PREFIX":                    config.AccessURLPrefix,
		"DELEGATE_DOWNLOAD_PREFIX":             config.DelegateDownloadPrefix,
		"SECRET_KEY":                           config.SecretKey,
		"LANGUAGE_CODE":                        config.LanguageCode,
		"TIME_ZONE":                            config.TimeZone,
		"SANITIZE_TAGS":                        strconv.FormatBool(config.SanitizeTags),
		"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS": strconv.FormatInt(config.SharedPlaylistDownloadMaxSeconds, 10),
		"MUSIC_IMPORT_DIR":                     config.MusicImportDir,
		"MAX_UPLOAD_BYTES":                     strconv.FormatInt(config.MaxUploadBytes, 10),
		"COOKIE_SECURE":                        strconv.FormatBool(config.CookieSecure),
		"LOG_LEVEL":                            strconv.Itoa(config.LogLevel),
		"CSRF_TRUSTED_ORIGINS":                 strings.Join(config.CSRFTrustedOrigins, ","),
		"LASTFM_API_KEY":                       config.LastFMAPIKey,
		"JAMA_ENDPOINT":                        config.JamaEndpoint,
		"JAMA_KEY":                             config.JamaKey,
		"JAMA_PROJECT":                         strconv.FormatInt(config.JamaProject, 10),
		"DBX_KEY":                              config.DropboxKey,
		"DBX_SECRET":                           config.DropboxSecret,
		"DBX_TOKEN":                            config.DropboxToken,
		"EMAIL_HOST":                           config.EmailHost,
		"EMAIL_PORT":                           strconv.Itoa(config.EmailPort),
		"EMAIL_USE_TLS":                        strconv.FormatBool(config.EmailUseTLS),
		"EMAIL_USE_SSL":                        strconv.FormatBool(config.EmailUseSSL),
		"EMAIL_HOST_USER":                      config.EmailHostUser,
		"EMAIL_HOST_PASSWORD":                  config.EmailHostPassword,
		"DEFAULT_FROM_EMAIL":                   config.DefaultFromEmail,
	}
	if config.JamaProject == 0 {
		values["JAMA_PROJECT"] = ""
	}
	return values
}

func printConfig(writer io.Writer, config *Config, showSecrets bool) error {
	keys := append([]string(nil), configKeys...)
	sort.Strings(keys)
	values := config.values()
	for _, key := range keys {
		value := values[key]
		switch key {
		case "SECRET_KEY", "LASTFM_API_KEY", "JAMA_KEY", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "EMAIL_HOST_PASSWORD":
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

func loadConfig(envFile, database string) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return loadConfigFromHome(home, envFile, database)
}

func loadConfigFromHome(home, envFile, database string) (*Config, error) {
	userDir := filepath.Join(home, ".reinhardt")
	values := map[string]string{
		"BASE_DIR": userDir, "LISTEN_ADDR": "127.0.0.1:8000",
		"LANGUAGE_CODE": "en-us", "TIME_ZONE": "UTC", "SANITIZE_TAGS": "true",
		"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS": "10800", "EMAIL_HOST": "localhost",
		"EMAIL_PORT": "25", "DEFAULT_FROM_EMAIL": "webmaster@localhost",
		"LOG_LEVEL": "30", "ALLOWED_HOSTS": "127.0.0.1,localhost", "MAX_UPLOAD_BYTES": "536870912",
		"LASTFM_API_KEY": defaultLastFMAPIKey,
	}
	// Merge all sources before validation: only effective values must be valid.
	// Process settings override .env, which overrides ~/.reinhardt/config.
	for _, file := range []string{filepath.Join(userDir, "config"), envFile} {
		if file == "" {
			continue
		}
		fileValues, err := godotenv.Read(file)
		if os.IsNotExist(err) && (file != envFile || envFile == ".env") {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		for _, key := range configKeys {
			if value, ok := fileValues[key]; ok {
				values[key] = value
			}
		}
	}
	for _, key := range configKeys {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if database != "" {
		values["DATABASE_PATH"] = database
	}
	return parseConfig(values)
}

func parseConfig(values map[string]string) (*Config, error) {
	config := &Config{
		BaseDir:                values["BASE_DIR"],
		DatabasePath:           values["DATABASE_PATH"],
		CoversDir:              values["COVERS_DIR"],
		ListenAddr:             values["LISTEN_ADDR"],
		AllowedHosts:           configList(values["ALLOWED_HOSTS"]),
		URLPrefix:              values["URL_PREFIX"],
		AccessURLPrefix:        values["ACCESS_URL_PREFIX"],
		DelegateDownloadPrefix: values["DELEGATE_DOWNLOAD_PREFIX"],
		SecretKey:              values["SECRET_KEY"],
		LanguageCode:           values["LANGUAGE_CODE"],
		TimeZone:               values["TIME_ZONE"],
		MusicImportDir:         values["MUSIC_IMPORT_DIR"],
		CSRFTrustedOrigins:     configList(values["CSRF_TRUSTED_ORIGINS"]),
		LastFMAPIKey:           values["LASTFM_API_KEY"],
		JamaEndpoint:           values["JAMA_ENDPOINT"],
		JamaKey:                values["JAMA_KEY"],
		DropboxKey:             values["DBX_KEY"],
		DropboxSecret:          values["DBX_SECRET"],
		DropboxToken:           values["DBX_TOKEN"],
		EmailHost:              values["EMAIL_HOST"],
		EmailHostUser:          values["EMAIL_HOST_USER"],
		EmailHostPassword:      values["EMAIL_HOST_PASSWORD"],
		DefaultFromEmail:       values["DEFAULT_FROM_EMAIL"],
	}
	for _, setting := range []struct {
		key    string
		target *bool
	}{
		{"SANITIZE_TAGS", &config.SanitizeTags},
		{"COOKIE_SECURE", &config.CookieSecure},
		{"EMAIL_USE_TLS", &config.EmailUseTLS},
		{"EMAIL_USE_SSL", &config.EmailUseSSL},
	} {
		value, err := configBool(setting.key, values[setting.key])
		if err != nil {
			return nil, err
		}
		*setting.target = value
	}
	var err error
	if config.MaxUploadBytes, err = configInteger("MAX_UPLOAD_BYTES", values["MAX_UPLOAD_BYTES"], 1, 0); err != nil {
		return nil, err
	}
	if config.SharedPlaylistDownloadMaxSeconds, err = configInteger("SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS", values["SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS"], 0, 0); err != nil {
		return nil, err
	}
	if value := values["JAMA_PROJECT"]; value != "" {
		if config.JamaProject, err = configInteger("JAMA_PROJECT", value, 1, 0); err != nil {
			return nil, err
		}
	}
	port, err := configInteger("EMAIL_PORT", values["EMAIL_PORT"], 1, 65535)
	if err != nil {
		return nil, err
	}
	config.EmailPort = int(port)
	level, err := strconv.Atoi(values["LOG_LEVEL"])
	if err != nil || level < 0 {
		return nil, fmt.Errorf("LOG_LEVEL must be a non-negative integer")
	}
	config.LogLevel = level
	config.URLPrefix = strings.Trim(config.URLPrefix, "/")
	if config.URLPrefix != "" {
		config.URLPrefix += "/"
	}
	if config.DatabasePath == "" {
		config.DatabasePath = filepath.Join(config.BaseDir, "db.sqlite3")
	}
	if config.CoversDir == "" {
		config.CoversDir = filepath.Join(config.BaseDir, "covers")
	}
	if _, err := time.LoadLocation(config.TimeZone); err != nil {
		return nil, fmt.Errorf("TIME_ZONE: %w", err)
	}
	return config, nil
}

func configBool(key, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean (true/false, yes/no, on/off, or 1/0)", key)
	}
}

// A zero maximum means there is no additional bound beyond int64's range.
func configInteger(key, value string, minimum, maximum int64) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minimum || (maximum != 0 && parsed > maximum) {
		if maximum != 0 {
			return 0, fmt.Errorf("%s must be an integer between %d and %d", key, minimum, maximum)
		}
		return 0, fmt.Errorf("%s must be an integer >= %d", key, minimum)
	}
	return parsed, nil
}

func configList(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func (config *Config) Path(path string) string { return "/" + config.URLPrefix + path }
