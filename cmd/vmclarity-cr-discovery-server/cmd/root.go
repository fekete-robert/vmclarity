package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openclarity/vmclarity/pkg/containerruntimediscovery"
	"github.com/openclarity/vmclarity/pkg/shared/log"
)

var (
	// Base logger.
	logger *logrus.Entry

	rootCmd = &cobra.Command{
		Use:          "vmclarity-cr-discovery-server",
		Short:        "Runs a server which provides endpoints for querying the container runtime.",
		Long:         "Runs a server which provides endpoints for querying the container runtime.",
		SilenceUsage: true, // Don't print the usage when an error is returned from RunE
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ctx = log.SetLoggerForContext(ctx, logger)

			discoverer, err := containerruntimediscovery.NewDiscoverer(ctx)
			if err != nil {
				return fmt.Errorf("unable to create discoverer: %w", err)
			}

			abortCtx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			// TODO(sambetts) Get listen address from the viper
			// configuration with a default
			listenAddr := ":8080"
			ids := containerruntimediscovery.NewContainerRuntimeDiscoveryServer(logger, listenAddr, discoverer)
			ids.Serve()

			logger.Infof("Server started listening on %s...", listenAddr)

			<-abortCtx.Done()

			logger.Infof("Shutting down...")

			shutdownContext, cancel := context.WithTimeout(ctx, 30*time.Second) // nolint:gomnd
			defer cancel()
			err = ids.Shutdown(shutdownContext)
			if err != nil {
				return fmt.Errorf("failed to shutdown server: %w", err)
			}

			logger.Infof("Successfully Shutdown. Goodbye.")

			return nil
		},
	}
)

// Execute executes the root command.
func Execute() error {
	// nolint: wrapcheck
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	log.InitLogger(logrus.InfoLevel.String(), os.Stderr)
	logger = logrus.WithField("app", "vmclarity")
}

func initConfig() {
	viper.AutomaticEnv()
}
