package reinhardt

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func TestTUICommandHelpAndArguments(test *testing.T) {
	for _, scenario := range []struct {
		args []string
		want string
	}{
		{args: []string{"--help"}, want: "full-screen terminal interface"},
		{args: []string{"unexpected"}, want: "accepts 0 arg(s)"},
		{want: "requires an interactive terminal"},
	} {
		if len(scenario.args) == 0 && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			continue
		}
		command, closeStore := newCLI()
		var output bytes.Buffer
		command.SetOut(&output)
		database := filepath.Join(test.TempDir(), "uncreated", "users.sqlite3")
		command.SetArgs(append([]string{"--database", database, "tui"}, scenario.args...))
		err := executeCLI(command, closeStore)
		if len(scenario.args) == 1 && scenario.args[0] == "--help" {
			if err != nil || !strings.Contains(output.String(), scenario.want) {
				test.Fatalf("help=%q, err=%v", output.String(), err)
			}
		} else if err == nil || !strings.Contains(err.Error(), scenario.want) {
			test.Fatalf("args=%v: err=%v", scenario.args, err)
		}
		if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
			test.Fatalf("command initialized database: %v", err)
		}
	}
}

func TestCLIExecutionClosesDatabase(test *testing.T) {
	commandErr := errors.New("command failed")
	closeErr := errors.New("database cleanup failed")
	for _, scenario := range []struct {
		name      string
		failStage string
		closeErr  error
	}{
		{name: "success"},
		{name: "pre-run-error", failStage: "pre-run"},
		{name: "run-error", failStage: "run"},
		{name: "post-run-error", failStage: "post-run"},
		{name: "cleanup-error", closeErr: closeErr},
		{name: "command-and-cleanup-errors", failStage: "run", closeErr: closeErr},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			app := testApp(test)
			command := &cobra.Command{
				Use: "cleanup-test", SilenceErrors: true, SilenceUsage: true,
				PreRunE: func(*cobra.Command, []string) error {
					if scenario.failStage == "pre-run" {
						return commandErr
					}
					return nil
				},
				RunE: func(*cobra.Command, []string) error {
					if err := app.Store.DB.PingContext(test.Context()); err != nil {
						test.Fatal("database closed before the command finished", err)
					}
					if scenario.failStage == "run" {
						return commandErr
					}
					return nil
				},
				PostRunE: func(*cobra.Command, []string) error {
					if scenario.failStage == "post-run" {
						return commandErr
					}
					return nil
				},
			}
			command.SetArgs([]string{})
			closeCalls := 0
			err := executeCLI(command, func() error {
				closeCalls++
				return errors.Join(app.Store.DB.Close(), scenario.closeErr)
			})
			if scenario.failStage != "" && !errors.Is(err, commandErr) {
				test.Fatalf("command error lost: %v", err)
			}
			if scenario.closeErr != nil && !errors.Is(err, scenario.closeErr) {
				test.Fatalf("cleanup error lost: %v", err)
			}
			if scenario.failStage == "" && scenario.closeErr == nil && err != nil {
				test.Fatal(err)
			}
			if closeCalls != 1 {
				test.Fatalf("cleanup calls=%d, want 1", closeCalls)
			}
			if err := app.Store.DB.PingContext(test.Context()); err == nil {
				test.Fatal("database remained open after CLI execution")
			}
		})
	}
}
