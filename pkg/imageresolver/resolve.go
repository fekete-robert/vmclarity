package imageresolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

func Resolve(ctx context.Context, config ResolverConfig) error {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	keychain, err := NewKeyChain(ctx, config.ImagePullSecretPath)
	if err != nil {
		return fmt.Errorf("unable to load keychain: %w", err)
	}

	imageSet := map[string]*models.ContainerImageInfo{}
	errs := []error{}
	for _, imageToResolve := range config.ImagesToResolve {
		image, err := ResolveImage(ctx, imageToResolve.Ref, imageToResolve.Os, imageToResolve.Arch, keychain)
		if err != nil {
			err = fmt.Errorf("unable to resolve image %+v: %w", imageToResolve, err)
			logger.Errorf("%v", err)
			errs = append(errs, err)
			continue
		}
		if original, ok := imageSet[*image.Id]; ok {
			image, err = models.MergeContainerImage(*original, *image)
			if err != nil {
				errs = append(errs, fmt.Errorf("unable to merge container image: %w", err))
				continue
			}
		}
		imageSet[*image.Id] = image
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

	_, err = http.Post(config.ResultEndpoint, "application/json", bytes.NewReader(resultBytes))
	if err != nil {
		return fmt.Errorf("unable to send results: %w", err)
	}

	return nil
}

func ResolveImage(ctx context.Context, r, os, arch string, keychain authn.Keychain) (*models.ContainerImageInfo, error) {
	// NOTE(sambetts) Check
	// https://github.com/google/go-containerregistry/blob/main/pkg/name/options.go
	// for other options that can be passed here
	ref, err := name.ParseReference(r)
	if err != nil {
		return nil, fmt.Errorf("unable to parse reference: %w", err)
	}

	// NOTE(sambetts) Check
	// https://github.com/google/go-containerregistry/blob/main/pkg/v1/remote/options.go
	// for other options that can be passed here. This is where we'll pass in the keychain.
	img, err := remote.Image(ref, remote.WithPlatform(v1.Platform{Architecture: arch, OS: os}))
	if err != nil {
		return nil, fmt.Errorf("unable to fetch image info from remote: %w", err)
	}

	imageId, err := img.ConfigName()
	if err != nil {
		return nil, fmt.Errorf("unable to image ID: %w", err)
	}

	size, err := img.Size()
	if err != nil {
		return nil, fmt.Errorf("unable to get image size: %w", err)
	}

	configfile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("unable to get config file from image: %w", err)
	}

	labels := []models.Tag{}
	for key, value := range configfile.Config.Labels {
		labels = append(labels, models.Tag{
			Key:   key,
			Value: value,
		})
	}

	repoDigests := []string{}
	repoTags := []string{}
	switch ref.(type) {
	case name.Digest:
		repoDigests = []string{ref.String()}
	case name.Tag:
		repoTags = []string{ref.String()}
	}

	return &models.ContainerImageInfo{
		Id:           utils.PointerTo(imageId.String()),
		Size:         utils.PointerTo(int(size)),
		Labels:       &labels,
		Os:           &configfile.OS,
		Architecture: &configfile.Architecture,
		RepoDigests:  &repoDigests,
		RepoTags:     &repoTags,
	}, nil
}
