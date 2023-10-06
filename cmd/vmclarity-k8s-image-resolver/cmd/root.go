package cmd

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/openclarity/vmclarity/pkg/imageresolver"
	"github.com/openclarity/vmclarity/pkg/shared/log"
)

var (
	// Used for flags.
	cfgFile string

	// Base logger
	logger *logrus.Entry

	rootCmd = &cobra.Command{
		Use:          "vmclarity-k8s-image-resolver",
		Short:        "Resolves a list of pullable image references to asset information",
		Long:         "Resolves a list of pullable image references to asset information",
		SilenceUsage: true, // Don't print the usage when an error is returned from RunE
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ctx = log.SetLoggerForContext(ctx, logger)

			var config imageresolver.ResolverConfig
			viper.Unmarshal(&config)

			err := imageresolver.Resolve(ctx, config)
			if err != nil {
				return fmt.Errorf("error occured while resolving images: %w", err)
			}

			return nil
		},
	}
)

// Execute executes the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file")

	log.InitLogger(logrus.InfoLevel.String(), os.Stderr)
	logger = logrus.WithField("app", "vmclarity")
}

func initConfig() {
	// Use config file from the flag.
	viper.SetConfigFile(cfgFile)
	viper.AutomaticEnv()
	if err := viper.ReadInConfig(); err == nil {
		fmt.Println("Using config file:", viper.ConfigFileUsed())
	}
}
