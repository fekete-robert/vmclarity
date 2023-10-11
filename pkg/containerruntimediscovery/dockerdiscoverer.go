package containerruntimediscovery

import (
	"context"
	"fmt"

	dtypes "github.com/docker/docker/api/types"
	dclient "github.com/docker/docker/client"
	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
	"github.com/sirupsen/logrus"
)

type DockerDiscoverer struct {
	client *dclient.Client
	logger *logrus.Entry
}

func NewDockerDiscoverer(ctx context.Context) (Discoverer, error) {
	dockerClient, err := dclient.NewClientWithOpts(dclient.FromEnv, dclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	_, err = dockerClient.ImageList(ctx, dtypes.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	return &DockerDiscoverer{
		client: dockerClient,
		logger: log.GetLoggerFromContextOrDefault(ctx),
	}, nil
}

func (dd *DockerDiscoverer) Images(ctx context.Context) ([]models.ContainerImageInfo, error) {
	// List all docker images
	images, err := dd.client.ImageList(ctx, dtypes.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	// Convert images to container image info
	result := make([]models.ContainerImageInfo, len(images))
	for i, image := range images {
		ii, err := dd.getContainerImageInfo(ctx, image.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to convert image to ContainerImageInfo: %w", err)
		}
		result[i] = ii
	}
	return result, nil
}

func (dd *DockerDiscoverer) getContainerImageInfo(ctx context.Context, imageID string) (models.ContainerImageInfo, error) {
	image, _, err := dd.client.ImageInspectWithRaw(ctx, imageID)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to inspect image: %w", err)
	}

	return models.ContainerImageInfo{
		Architecture: utils.PointerTo(image.Architecture),
		Id:           utils.PointerTo(image.ID),
		Labels:       convertTags(image.Config.Labels),
		RepoTags:     &image.RepoTags,
		RepoDigests:  &image.RepoDigests,
		ObjectType:   "ContainerImageInfo",
		Os:           utils.PointerTo(image.Os),
		Size:         utils.PointerTo(int(image.Size)),
	}, nil
}

func convertTags(tags map[string]string) *[]models.Tag {
	ret := make([]models.Tag, 0, len(tags))
	for key, val := range tags {
		ret = append(ret, models.Tag{
			Key:   key,
			Value: val,
		})
	}
	return &ret
}
