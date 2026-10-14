package reinhardt

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

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
