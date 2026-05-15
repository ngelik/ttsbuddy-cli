package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var completionNoDesc bool

var completionCmd = &cobra.Command{
	Use:   "completion",
	Short: "Generate the autocompletion script for the specified shell",
	Long: `Generate the autocompletion script for ttsbuddy for the specified shell.
See each sub-command's help for details on how to use the generated script.`,
	Args: noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SetOut(os.Stdout)
		return cmd.Help()
	},
}

var completionBashCmd = &cobra.Command{
	Use:                   "bash",
	Short:                 "Generate the autocompletion script for bash",
	Long:                  completionLong("bash", "Generate the autocompletion script for the bash shell."),
	Args:                  noArgs,
	DisableFlagsInUseLine: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Root().GenBashCompletionV2(os.Stdout, !completionNoDesc)
	},
}

var completionZshCmd = &cobra.Command{
	Use:   "zsh",
	Short: "Generate the autocompletion script for zsh",
	Long:  completionLong("zsh", "Generate the autocompletion script for the zsh shell."),
	Args:  noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if completionNoDesc {
			return cmd.Root().GenZshCompletionNoDesc(os.Stdout)
		}
		return cmd.Root().GenZshCompletion(os.Stdout)
	},
}

var completionFishCmd = &cobra.Command{
	Use:   "fish",
	Short: "Generate the autocompletion script for fish",
	Long:  completionLong("fish", "Generate the autocompletion script for the fish shell."),
	Args:  noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Root().GenFishCompletion(os.Stdout, !completionNoDesc)
	},
}

var completionPowerShellCmd = &cobra.Command{
	Use:   "powershell",
	Short: "Generate the autocompletion script for powershell",
	Long:  completionLong("powershell", "Generate the autocompletion script for powershell."),
	Args:  noArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if completionNoDesc {
			return cmd.Root().GenPowerShellCompletion(os.Stdout)
		}
		return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
	},
}

func init() {
	for _, cmd := range []*cobra.Command{completionBashCmd, completionZshCmd, completionFishCmd, completionPowerShellCmd} {
		cmd.Flags().BoolVar(&completionNoDesc, "no-descriptions", false, "disable completion descriptions")
	}
	completionCmd.AddCommand(completionBashCmd, completionZshCmd, completionFishCmd, completionPowerShellCmd)
	rootCmd.AddCommand(completionCmd)
}

func completionLong(shell, intro string) string {
	return fmt.Sprintf(`%s

Print the completion script to stdout:

	ttsbuddy completion %s

Redirect or source that output according to your %s setup.`, intro, shell, shell)
}

func isCompletionCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "completion" {
			return true
		}
	}
	return false
}
