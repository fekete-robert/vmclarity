package cmd

import (
	"fmt"
	"os"
	"net/http"
	"encoding/json"
	"bytes"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/sirupsen/logrus"

	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/imageresolver"
	"github.com/openclarity/vmclarity/api/models"
)

type ImageToResolve struct {
	Ref string `yaml:"ref"`
	Os string `yaml:"os"`
	Arch string `yaml:"arch"`
}

type ResolverConfig struct {
	ImagesToResolve []ImageToResolve `yaml:"imagesToResolve"`
	ImagePullSecretPath string `yaml:"imagePullSecretPath"`
	ResultEndpoint string `yaml:"resultEndpoint"`
}

type ResolverResponse struct {
	Images []models.ContainerImageInfo
	Errors []string
}

var (
	// Used for flags.
	cfgFile     string

	// Base logger
	logger *logrus.Entry

	rootCmd = &cobra.Command{
		Use:   "vmclarity-k8s-image-resolver",
		Short: "Resolves a list of pullable image references to asset information",
		Long: "Resolves a list of pullable image references to asset information",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			ctx = log.SetLoggerForContext(ctx, logger)

			var config ResolverConfig
			viper.Unmarshal(&config)

			keychain, err := imageresolver.NewKeyChain(ctx, config.ImagePullSecretPath)
			if err != nil {
				return fmt.Errorf("unable to load keychain: %w", err)
			}

			imageSet := map[string]*models.ContainerImageInfo{}
			errs := []error{}
			for _, imageToResolve := range config.ImagesToResolve {
				image, err := imageresolver.Resolve(ctx, imageToResolve.Ref, imageToResolve.Os, imageToResolve.Arch, keychain)
				if err != nil {
					err = fmt.Errorf("unable to resolve image %+v: %w", imageToResolve, err)
					logger.Errorf("%v", err)
					errs = append(errs, err)
					continue
				}
				if original, ok := imageSet[*image.Id]; ok {
					mergedImage, err := mergeContainerImage(*original, *image)
					if err != nil {
						errs = append(errs, fmt.Errorf("unable to merge container image: %w", err))
						continue
					}
					imageSet[*image.Id] = mergedImage
				} else {
					imageSet[*image.Id] = image
				}
			}

			images := []models.ContainerImageInfo{}
			for _, image := range imageSet {
				images = append(images, *image)
			}
			errStrings := []string{}
			for _, err := range errs {
				errStrings = append(errStrings, err.Error())
			}

			response := ResolverResponse{
				Images: images,
				Errors: errStrings,
			}

			resultBytes, err := json.Marshal(response)
			if err != nil {
				return fmt.Errorf("unable to marshal response: %w", err)
			}

			resp, err := http.Post(config.ResultEndpoint, "application/json", bytes.NewReader(resultBytes))
			if err != nil {
				return fmt.Errorf("unable to send results: %w", err)
			}

			logger.Infof("response received: %v", resp.StatusCode)

			return nil
		},
	}
)

func mergeComparable[T comparable](original, new T) (T, error) {
	var zero T
	if original != zero && new != zero && original != new {
		return zero, fmt.Errorf("%v does not match %v", original, new)
	}
	if original != zero {
		return original, nil
	}
	return new, nil
}

func unionSlices[T comparable](original, new []T) []T {
	n := map[T]struct{}{}
	for _, i := range original {
		n[i] = struct{}{}
	}
	for _, i := range new {
		n[i] = struct{}{}
	}

	output := []T{}
	for i := range n {
		output = append(output, i)
	}
	return output
}

func mergeContainerImage(original, new models.ContainerImageInfo) (*models.ContainerImageInfo, error) {
	id, err := mergeComparable(*original.Id, *new.Id)
	if err != nil {
		return nil, fmt.Errorf("failed to merge Id field: %w", err)
	}

	size, err := mergeComparable(*original.Size, *new.Size)
	if err != nil {
		return nil, fmt.Errorf("failed to merge Size field: %w", err)
	}

	os, err := mergeComparable(*original.Os, *new.Os)
	if err != nil {
		return nil, fmt.Errorf("failed to merge Os field: %w", err)
	}

	architecture, err := mergeComparable(*original.Architecture, *new.Architecture)
	if err != nil {
		return nil, fmt.Errorf("failed to merge Architecture field: %w", err)
	}

	labels := unionSlices(*original.Labels, *new.Labels)

	repoDigests := unionSlices(*original.RepoDigests, *new.RepoDigests)

	repoTags := unionSlices(*original.RepoTags, *new.RepoTags)

	return &models.ContainerImageInfo{
		Id: &id,
		Size: &size,
		Labels: &labels,
		Os: &os,
		Architecture: &architecture,
		RepoDigests: &repoDigests,
		RepoTags: &repoTags,
	}, nil
}

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
