// Command streamwreck is the CLI entrypoint. It orchestrates the lab from the
// host: bringing the compose stack up/down and driving encoder/shaper/verifier
// containers over `docker` (see internal/run).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "streamwreck",
		Short:         "Spin up livestreams and subject them to reproducible adverse conditions",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("compose", defaultComposeFile(), "path to docker-compose.yml")
	root.PersistentFlags().String("project", "streamwreck", "docker compose project name")

	root.AddCommand(
		initCmd(),
		runCmd(),
		validateCmd(),
		presetsCmd(),
		reportCmd(),
		upCmd(),
		downCmd(),
	)
	return root
}
