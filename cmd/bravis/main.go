package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	version = "0.1.0"
	commit  = "dev"
)

var rootCmd = &cobra.Command{
	Use:     "bravis",
	Short:   "Extract and load data to BigQuery",
	Long:    "Bravis CLI: High-performance data extraction and loading. No schema opinions.",
	Version: fmt.Sprintf("%s (%s)", version, commit),
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(
		extractCmd,
		loadCmd,
		runCmd,
		versionCmd,
	)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
