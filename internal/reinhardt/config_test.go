package reinhardt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

func TestConfigCLI(test *testing.T) {
	for _, reveal := range []bool{false, true} {
		test.Run(fmt.Sprintf("show-secrets=%v", reveal), func(test *testing.T) {
			unsetConfigEnvironment(test, configKeys...)
			root := test.TempDir()
			file := filepath.Join(root, ".env")
			fileDB := filepath.Join(root, "from-file.sqlite3")
			flagDB := filepath.Join(root, "uncreated", "from-flag.sqlite3")
			values := Config{
				"BASE_DIR": root, "DATABASE_PATH": fileDB, "TIME_ZONE": "UTC",
				"URL_PREFIX": "/music/", "EMAIL_PORT": "2525",
				"SECRET_KEY": "test-secret", "JAMA_KEY": "test-jama-key",
				"LASTFM_API_KEY": "test-lastfm-key",
				"DBX_KEY":        "test-dbx-key", "DBX_SECRET": "test-dbx-secret",
				"DBX_TOKEN":           "test-dbx-token",
				"EMAIL_HOST_PASSWORD": "test-smtp-password",
			}
			var dotenv strings.Builder
			for _, key := range configKeys {
				fmt.Fprintf(&dotenv, "%s=%q\n", key, values[key])
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
			cli := newCLI()
			var out bytes.Buffer
			cli.SetOut(&out)
			cli.SetArgs(args)
			if err := cli.Execute(); err != nil {
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
		if config[key] != want {
			test.Errorf("%s = %q; want %q", key, config[key], want)
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
			if config["BASE_DIR"] != base || config["DATABASE_PATH"] != filepath.Join(base, "db.sqlite3") || config["COVERS_DIR"] != filepath.Join(base, "covers") {
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
			if config["DATABASE_PATH"] != db || config["COVERS_DIR"] != covers {
				test.Fatal("explicit data paths were overridden")
			}
			flagDB := filepath.Join(home, "flag.sqlite3")
			config, err = loadConfigFromHome(home, envFile, flagDB)
			if err != nil {
				test.Fatal(err)
			}
			if config["DATABASE_PATH"] != flagDB {
				test.Fatal("database flag did not override the environment")
			}
		})
	}
}
