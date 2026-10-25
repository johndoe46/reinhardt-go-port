package reinhardt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func unsetConfigEnvironment(test *testing.T, keys ...string) {
	test.Helper()
	for _, key := range keys {
		test.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			test.Fatal(err)
		}
	}
}

func TestYTDLPCookiesFileConfig(test *testing.T) {
	for _, source := range []string{"default", "dotenv", "environment"} {
		test.Run(source, func(test *testing.T) {
			unsetConfigEnvironment(test, configKeys...)
			home := test.TempDir()
			envFile := filepath.Join(home, ".env")
			want := ""
			contents := ""
			if source != "default" {
				want = filepath.Join(home, "file cookies.txt")
				contents = fmt.Sprintf("YTDLP_COOKIES_FILE=%q\n", want)
			}
			if err := os.WriteFile(envFile, []byte(contents), 0600); err != nil {
				test.Fatal(err)
			}
			if source == "environment" {
				want = filepath.Join(home, "environment cookies.txt")
				test.Setenv("YTDLP_COOKIES_FILE", want)
			}
			config, err := loadConfigFromHome(home, envFile, "")
			if err != nil {
				test.Fatal(err)
			}
			if config.YTDLPCookiesFile != want || config.values()["YTDLP_COOKIES_FILE"] != want {
				test.Fatalf("cookies setting = %q, want %q", config.YTDLPCookiesFile, want)
			}
		})
	}
}

func TestConfigCLI(test *testing.T) {
	for _, reveal := range []bool{false, true} {
		test.Run(fmt.Sprintf("show-secrets=%v", reveal), func(test *testing.T) {
			unsetConfigEnvironment(test, configKeys...)
			root := test.TempDir()
			file := filepath.Join(root, ".env")
			fileDB := filepath.Join(root, "from-file.sqlite3")
			flagDB := filepath.Join(root, "uncreated", "from-flag.sqlite3")
			values := map[string]string{
				"BASE_DIR": root, "DATABASE_PATH": fileDB, "TIME_ZONE": "UTC",
				"URL_PREFIX": "/music/", "EMAIL_PORT": "2525",
				"SECRET_KEY": "test-secret", "JAMA_KEY": "test-jama-key",
				"LASTFM_API_KEY": "test-lastfm-key",
				"DBX_KEY":        "test-dbx-key", "DBX_SECRET": "test-dbx-secret",
				"DBX_TOKEN":           "test-dbx-token",
				"EMAIL_HOST_PASSWORD": "test-smtp-password",
			}
			var dotenv strings.Builder
			for key, value := range values {
				fmt.Fprintf(&dotenv, "%s=%q\n", key, value)
			}
			if err := os.WriteFile(file, []byte(dotenv.String()), 0600); err != nil {
				test.Fatal(err)
			}
			test.Setenv("EMAIL_PORT", "1025")
			test.Setenv("UNRELATED_PASSWORD", "unrelated-sensitive-value")
			args := []string{"--env-file", file, "--database", flagDB, "config"}
			if reveal {
				args = append(args, "--show-secrets")
			}
			cli, closeStore := newCLI()
			var out bytes.Buffer
			cli.SetOut(&out)
			cli.SetArgs(args)
			if err := executeCLI(cli, closeStore); err != nil {
				test.Fatal(err)
			}
			output := out.String()
			for key, want := range map[string]string{
				"BASE_DIR": root, "DATABASE_PATH": flagDB,
				"COVERS_DIR": filepath.Join(root, "covers"), "EMAIL_PORT": "1025", "URL_PREFIX": "music/",
			} {
				if !strings.Contains(output, fmt.Sprintf("%s=%q\n", key, want)) {
					test.Errorf("missing effective value for %s", key)
				}
			}
			for _, key := range []string{"SECRET_KEY", "LASTFM_API_KEY", "JAMA_KEY", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "EMAIL_HOST_PASSWORD"} {
				want := "[redacted]"
				if reveal {
					want = values[key]
				} else if strings.Contains(output, values[key]) {
					test.Errorf("credential leaked: %s", key)
				}
				if !strings.Contains(output, fmt.Sprintf("%s=%q\n", key, want)) {
					test.Errorf("incorrect credential output for %s", key)
				}
			}
			if strings.Contains(output, "UNRELATED_PASSWORD") || strings.Contains(output, "unrelated-sensitive-value") {
				test.Fatal("unrelated environment included in configuration output")
			}
			for _, path := range []string{fileDB, filepath.Dir(flagDB), filepath.Join(root, "covers")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					test.Errorf("config command created %s", path)
				}
			}
		})
	}
}

func TestConfigDefaultsToUserDirectory(test *testing.T) {
	unsetConfigEnvironment(test, "BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "SECRET_KEY", "TIME_ZONE")
	home := test.TempDir()
	userDir := filepath.Join(home, ".reinhardt")
	if err := os.Mkdir(userDir, 0700); err != nil {
		test.Fatal(err)
	}
	configFile := filepath.Join(userDir, "config")
	original := []byte("SECRET_KEY=existing-secret\nTIME_ZONE=Europe/Paris\n")
	if err := os.WriteFile(configFile, original, 0600); err != nil {
		test.Fatal(err)
	}
	config, err := loadConfigFromHome(home, "", "")
	if err != nil {
		test.Fatal(err)
	}
	for key, want := range map[string]string{
		"BASE_DIR": userDir, "DATABASE_PATH": filepath.Join(userDir, "db.sqlite3"),
		"COVERS_DIR": filepath.Join(userDir, "covers"),
		"SECRET_KEY": "existing-secret", "TIME_ZONE": "Europe/Paris",
	} {
		if config.values()[key] != want {
			test.Errorf("%s = %q; want %q", key, config.values()[key], want)
		}
	}
	contents, err := os.ReadFile(configFile)
	if err != nil || string(contents) != string(original) {
		test.Fatalf("existing configuration changed: %v", err)
	}
}

func TestConfigDataDirectoryOverrides(test *testing.T) {
	for _, source := range []string{"config", "dotenv", "environment"} {
		test.Run(source, func(test *testing.T) {
			unsetConfigEnvironment(test, "BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "TIME_ZONE")
			home := test.TempDir()
			base := filepath.Join(home, ".go-reinhardt")
			envFile := ""
			if source == "environment" {
				test.Setenv("BASE_DIR", base)
			} else {
				file := filepath.Join(home, ".reinhardt", "config")
				if source == "dotenv" {
					file = filepath.Join(home, ".env")
					envFile = file
				}
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					test.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("BASE_DIR="+base+"\n"), 0600); err != nil {
					test.Fatal(err)
				}
			}
			config, err := loadConfigFromHome(home, envFile, "")
			if err != nil {
				test.Fatal(err)
			}
			if config.BaseDir != base || config.DatabasePath != filepath.Join(base, "db.sqlite3") || config.CoversDir != filepath.Join(base, "covers") {
				test.Fatal("data paths did not follow BASE_DIR override")
			}
			db := filepath.Join(home, "custom.sqlite3")
			covers := filepath.Join(home, "custom-covers")
			test.Setenv("DATABASE_PATH", db)
			test.Setenv("COVERS_DIR", covers)
			config, err = loadConfigFromHome(home, envFile, "")
			if err != nil {
				test.Fatal(err)
			}
			if config.DatabasePath != db || config.CoversDir != covers {
				test.Fatal("explicit data paths were overridden")
			}
			flagDB := filepath.Join(home, "flag.sqlite3")
			config, err = loadConfigFromHome(home, envFile, flagDB)
			if err != nil {
				test.Fatal(err)
			}
			if config.DatabasePath != flagDB {
				test.Fatal("database flag did not override the environment")
			}
		})
	}
}

func TestConfigTypedDefaultsAndLists(test *testing.T) {
	unsetConfigEnvironment(test, configKeys...)
	config, err := loadConfigFromHome(test.TempDir(), "", "")
	if err != nil {
		test.Fatal(err)
	}
	if !config.SanitizeTags || config.CookieSecure || config.EmailUseTLS || config.EmailUseSSL || config.MaxUploadBytes != 536870912 || config.SharedPlaylistDownloadMaxSeconds != 10800 || config.EmailPort != 25 || config.LogLevel != 30 || config.JamaProject != 0 {
		test.Fatal("typed defaults changed")
	}
	if !reflect.DeepEqual(config.AllowedHosts, []string{"127.0.0.1", "localhost"}) || len(config.CSRFTrustedOrigins) != 0 {
		test.Fatal("default host or origin lists changed")
	}
	test.Setenv("ALLOWED_HOSTS", " localhost, .example.com, ,127.0.0.1 ")
	test.Setenv("CSRF_TRUSTED_ORIGINS", " https://one.example, https://two.example, ")
	config, err = loadConfigFromHome(test.TempDir(), "", "")
	if err != nil || !reflect.DeepEqual(config.AllowedHosts, []string{"localhost", ".example.com", "127.0.0.1"}) || !reflect.DeepEqual(config.CSRFTrustedOrigins, []string{"https://one.example", "https://two.example"}) {
		test.Fatal("comma-separated settings were not parsed", err)
	}
}

func TestConfigRejectsInvalidSettingsBeforeOpeningDatabase(test *testing.T) {
	for _, scenario := range []struct{ key, value string }{
		{"MAX_UPLOAD_BYTES", "invalid"},
		{"MAX_UPLOAD_BYTES", ""},
		{"MAX_UPLOAD_BYTES", "0"},
		{"MAX_UPLOAD_BYTES", "-1"},
		{"MAX_UPLOAD_BYTES", "9223372036854775808"},
		{"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS", "1.5"},
		{"SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS", "-1"},
		{"LOG_LEVEL", "invalid"},
		{"LOG_LEVEL", "-1"},
		{"EMAIL_PORT", "invalid"},
		{"EMAIL_PORT", "0"},
		{"EMAIL_PORT", "65536"},
		{"JAMA_PROJECT", "invalid"},
		{"JAMA_PROJECT", "0"},
		{"JAMA_PROJECT", "-1"},
		{"SANITIZE_TAGS", "maybe"},
		{"COOKIE_SECURE", "maybe"},
		{"EMAIL_USE_TLS", "maybe"},
		{"EMAIL_USE_SSL", "maybe"},
	} {
		test.Run(scenario.key+"="+scenario.value, func(test *testing.T) {
			unsetConfigEnvironment(test, configKeys...)
			root := test.TempDir()
			file := filepath.Join(root, ".env")
			if err := os.WriteFile(file, []byte(fmt.Sprintf("BASE_DIR=%q\n%s=%q\n", root, scenario.key, scenario.value)), 0600); err != nil {
				test.Fatal(err)
			}
			for _, command := range []string{"config", "migrate"} {
				cli, closeStore := newCLI()
				cli.SetArgs([]string{"--env-file", file, command})
				if err := executeCLI(cli, closeStore); err == nil || !strings.Contains(err.Error(), scenario.key) {
					test.Fatalf("%s did not report invalid %s: %v", command, scenario.key, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "db.sqlite3")); !os.IsNotExist(err) {
				test.Fatal("invalid configuration initialized the database", err)
			}
		})
	}
}

func TestConfigValidatesOnlyEffectiveOverrides(test *testing.T) {
	unsetConfigEnvironment(test, configKeys...)
	home := test.TempDir()
	userDir := filepath.Join(home, ".reinhardt")
	if err := os.Mkdir(userDir, 0700); err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "config"), []byte("MAX_UPLOAD_BYTES=invalid\nCOOKIE_SECURE=invalid\n"), 0600); err != nil {
		test.Fatal(err)
	}
	file := filepath.Join(home, ".env")
	if err := os.WriteFile(file, []byte("MAX_UPLOAD_BYTES=1024\nCOOKIE_SECURE=off\n"), 0600); err != nil {
		test.Fatal(err)
	}
	config, err := loadConfigFromHome(home, file, "")
	if err != nil || config.MaxUploadBytes != 1024 || config.CookieSecure {
		test.Fatal("dotenv override was not applied before validation", err)
	}
	test.Setenv("MAX_UPLOAD_BYTES", "2048")
	test.Setenv("COOKIE_SECURE", "YES")
	config, err = loadConfigFromHome(home, file, "")
	if err != nil || config.MaxUploadBytes != 2048 || !config.CookieSecure {
		test.Fatal("process override was not applied before validation", err)
	}
	test.Setenv("MAX_UPLOAD_BYTES", "invalid")
	if _, err = loadConfigFromHome(home, file, ""); err == nil || !strings.Contains(err.Error(), "MAX_UPLOAD_BYTES") {
		test.Fatal("invalid effective environment override was accepted", err)
	}
}

func TestConfigBooleanSpellings(test *testing.T) {
	for _, key := range []string{"SANITIZE_TAGS", "COOKIE_SECURE", "EMAIL_USE_TLS", "EMAIL_USE_SSL"} {
		for _, value := range []string{"true", "TRUE", "1", "yes", "YES", "on", "false", "FALSE", "0", "no", "NO", "off", ""} {
			test.Run(key+"="+value, func(test *testing.T) {
				unsetConfigEnvironment(test, configKeys...)
				test.Setenv(key, value)
				config, err := loadConfigFromHome(test.TempDir(), "", "")
				if err != nil {
					test.Fatal(err)
				}
				got := map[string]bool{"SANITIZE_TAGS": config.SanitizeTags, "COOKIE_SECURE": config.CookieSecure, "EMAIL_USE_TLS": config.EmailUseTLS, "EMAIL_USE_SSL": config.EmailUseSSL}[key]
				want := value == "true" || value == "TRUE" || value == "1" || value == "yes" || value == "YES" || value == "on"
				if got != want {
					test.Fatalf("boolean=%v, want %v", got, want)
				}
			})
		}
	}
}
