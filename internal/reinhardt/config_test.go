package reinhardt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func unsetConfigEnvironment(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigCLI(t *testing.T) {
	for _, reveal := range []bool{false, true} {
		t.Run(fmt.Sprintf("show-secrets=%v", reveal), func(t *testing.T) {
			unsetConfigEnvironment(t, configKeys...)
			root := t.TempDir()
			file := filepath.Join(root, ".env")
			fileDB := filepath.Join(root, "from-file.sqlite3")
			flagDB := filepath.Join(root, "uncreated", "from-flag.sqlite3")
			values := Config{
				"BASE_DIR": root, "DATABASE_PATH": fileDB, "TIME_ZONE": "UTC",
				"URL_PREFIX": "/music/", "EMAIL_PORT": "2525",
				"SECRET_KEY": "test-secret", "JAMA_KEY": "test-jama-key",
				"DBX_KEY": "test-dbx-key", "DBX_SECRET": "test-dbx-secret",
				"DBX_TOKEN": "test-dbx-token", "DISCOGS_TOKEN": "test-discogs-token",
				"EMAIL_HOST_PASSWORD": "test-smtp-password",
			}
			var dotenv strings.Builder
			for _, key := range configKeys {
				fmt.Fprintf(&dotenv, "%s=%q\n", key, values[key])
			}
			if err := os.WriteFile(file, []byte(dotenv.String()), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EMAIL_PORT", "1025")
			t.Setenv("UNRELATED_PASSWORD", "unrelated-sensitive-value")
			args := []string{"--env-file", file, "--database", flagDB, "config"}
			if reveal {
				args = append(args, "--show-secrets")
			}
			cli := newCLI()
			var out bytes.Buffer
			cli.SetOut(&out)
			cli.SetArgs(args)
			if err := cli.Execute(); err != nil {
				t.Fatal(err)
			}
			output := out.String()
			for key, want := range map[string]string{
				"BASE_DIR": root, "DATABASE_PATH": flagDB,
				"COVERS_DIR": filepath.Join(root, "covers"), "EMAIL_PORT": "1025", "URL_PREFIX": "music/",
			} {
				if !strings.Contains(output, fmt.Sprintf("%s=%q\n", key, want)) {
					t.Errorf("missing effective value for %s", key)
				}
			}
			for _, key := range []string{"SECRET_KEY", "JAMA_KEY", "DBX_KEY", "DBX_SECRET", "DBX_TOKEN", "DISCOGS_TOKEN", "EMAIL_HOST_PASSWORD"} {
				want := "[redacted]"
				if reveal {
					want = values[key]
				} else if strings.Contains(output, values[key]) {
					t.Errorf("credential leaked: %s", key)
				}
				if !strings.Contains(output, fmt.Sprintf("%s=%q\n", key, want)) {
					t.Errorf("incorrect credential output for %s", key)
				}
			}
			if strings.Contains(output, "UNRELATED_PASSWORD") || strings.Contains(output, "unrelated-sensitive-value") {
				t.Fatal("unrelated environment included in configuration output")
			}
			for _, path := range []string{fileDB, filepath.Dir(flagDB), filepath.Join(root, "covers")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("config command created %s", path)
				}
			}
		})
	}
}

func TestConfigDefaultsToDjangoUserDirectory(t *testing.T) {
	unsetConfigEnvironment(t, "BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "SECRET_KEY", "TIME_ZONE")
	home := t.TempDir()
	userDir := filepath.Join(home, ".reinhardt")
	if err := os.Mkdir(userDir, 0700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(userDir, "config")
	original := []byte("SECRET_KEY=existing-django-secret\nTIME_ZONE=Europe/Paris\n")
	if err := os.WriteFile(configFile, original, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfigFromHome(home, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"BASE_DIR": userDir, "DATABASE_PATH": filepath.Join(userDir, "db.sqlite3"),
		"COVERS_DIR": filepath.Join(userDir, "covers"),
		"SECRET_KEY": "existing-django-secret", "TIME_ZONE": "Europe/Paris",
	} {
		if c[key] != want {
			t.Errorf("%s = %q; want %q", key, c[key], want)
		}
	}
	contents, err := os.ReadFile(configFile)
	if err != nil || string(contents) != string(original) {
		t.Fatalf("existing configuration changed: %v", err)
	}
}

func TestConfigDataDirectoryOverrides(t *testing.T) {
	for _, source := range []string{"config", "dotenv", "environment"} {
		t.Run(source, func(t *testing.T) {
			unsetConfigEnvironment(t, "BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "TIME_ZONE")
			home := t.TempDir()
			base := filepath.Join(home, ".go-reinhardt")
			envFile := ""
			if source == "environment" {
				t.Setenv("BASE_DIR", base)
			} else {
				file := filepath.Join(home, ".reinhardt", "config")
				if source == "dotenv" {
					file = filepath.Join(home, ".env")
					envFile = file
				}
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("BASE_DIR="+base+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := loadConfigFromHome(home, envFile, "")
			if err != nil {
				t.Fatal(err)
			}
			if c["BASE_DIR"] != base || c["DATABASE_PATH"] != filepath.Join(base, "db.sqlite3") || c["COVERS_DIR"] != filepath.Join(base, "covers") {
				t.Fatal("data paths did not follow BASE_DIR override")
			}
			db := filepath.Join(home, "custom.sqlite3")
			covers := filepath.Join(home, "custom-covers")
			t.Setenv("DATABASE_PATH", db)
			t.Setenv("COVERS_DIR", covers)
			c, err = loadConfigFromHome(home, envFile, "")
			if err != nil {
				t.Fatal(err)
			}
			if c["DATABASE_PATH"] != db || c["COVERS_DIR"] != covers {
				t.Fatal("explicit data paths were overridden")
			}
			flagDB := filepath.Join(home, "flag.sqlite3")
			c, err = loadConfigFromHome(home, envFile, flagDB)
			if err != nil {
				t.Fatal(err)
			}
			if c["DATABASE_PATH"] != flagDB {
				t.Fatal("database flag did not override the environment")
			}
		})
	}
}
