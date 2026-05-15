package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

type helpShownError struct {
	*exitError
}

func exactArgs(n int) cobra.PositionalArgs {
	return helpOnArgError(cobra.ExactArgs(n))
}

func maxArgs(n int) cobra.PositionalArgs {
	return helpOnArgError(cobra.MaximumNArgs(n))
}

func noArgs(cmd *cobra.Command, args []string) error {
	return helpOnArgError(cobra.NoArgs)(cmd, args)
}

func helpOnArgError(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return showHelpForUsageError(cmd, err)
		}
		return nil
	}
}

func helpOnFlagError(cmd *cobra.Command, err error) error {
	return showHelpForUsageError(cmd, err)
}

func showHelpForUsageError(cmd *cobra.Command, err error) error {
	wrapped := &exitError{code: 2, msg: err.Error()}
	if flagJSON {
		return wrapped
	}
	cmd.SetOut(os.Stderr)
	if helpErr := cmd.Help(); helpErr != nil {
		return &exitError{code: 2, msg: fmt.Sprintf("%s; additionally failed to show help: %v", err, helpErr)}
	}
	return &helpShownError{exitError: wrapped}
}
