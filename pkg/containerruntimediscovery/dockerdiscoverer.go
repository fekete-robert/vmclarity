// Copyright © 2023 Cisco Systems, Inc. and its affiliates.
// All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package containerruntimediscovery

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	dtypes "github.com/docker/docker/api/types"
	dtypesContainer "github.com/docker/docker/api/types/container"
	dclient "github.com/docker/docker/client"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type DockerDiscoverer struct {
	client *dclient.Client
}

func NewDockerDiscoverer(ctx context.Context) (Discoverer, error) {
	client, err := dclient.NewClientWithOpts(dclient.FromEnv, dclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	_, err = client.ImageList(ctx, dtypes.ImageListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	return &DockerDiscoverer{
		client: client,
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

func (dd *DockerDiscoverer) Image(ctx context.Context, imageID string) (models.ContainerImageInfo, error) {
	info, err := dd.getContainerImageInfo(ctx, imageID)
	if dclient.IsErrNotFound(err) {
		return info, ErrNotFound
	}
	return info, err
}

func (dd *DockerDiscoverer) getContainerImageInfo(ctx context.Context, imageID string) (models.ContainerImageInfo, error) {
	image, _, err := dd.client.ImageInspectWithRaw(ctx, imageID)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to inspect image: %w", err)
	}

	return models.ContainerImageInfo{
		Architecture: utils.PointerTo(image.Architecture),
		ImageID:      image.ID,
		Labels:       convertTags(image.Config.Labels),
		RepoTags:     &image.RepoTags,
		RepoDigests:  &image.RepoDigests,
		ObjectType:   "ContainerImageInfo",
		Os:           utils.PointerTo(image.Os),
		Size:         utils.PointerTo(int(image.Size)),
	}, nil
}

func (dd *DockerDiscoverer) ExportImage(ctx context.Context, imageID string, output io.Writer) error {
	reader, err := dd.client.ImageSave(ctx, []string{imageID})
	if err != nil {
		return fmt.Errorf("unable to save image from daemon: %w", err)
	}
	defer reader.Close()

	_, err = io.Copy(output, reader)
	if err != nil {
		return fmt.Errorf("failed to copy image to output: %w", err)
	}

	return nil
}

func (dd *DockerDiscoverer) ExportImageFilesystem(ctx context.Context, imageID string) (io.ReadCloser, func(), error) {
	clean := &cleanuper{}
	defer clean.ifNotSuccessful()

	logger := log.GetLoggerFromContextOrDiscard(ctx)

	// Create an ephemeral container to export asset
	containerResp, err := dd.client.ContainerCreate(
		ctx,
		&dtypesContainer.Config{Image: imageID},
		nil,
		nil,
		nil,
		"",
	)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to create helper container: %w", err)
	}

	clean.add(func() {
		err := dd.client.ContainerRemove(ctx, containerResp.ID, dtypes.ContainerRemoveOptions{Force: true})
		if err != nil {
			logger.Errorf("failed to remove helper container=%s: %v", containerResp.ID, err)
		}
	})

	contents, err := dd.client.ContainerExport(ctx, containerResp.ID)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to export container: %w", err)
	}

	// Pipe container export reader through gzip
	r, w := io.Pipe()
	go func() {
		defer w.Close()

		gzipWriter := gzip.NewWriter(w)
		defer gzipWriter.Close()

		_, err := io.Copy(gzipWriter, contents)
		if err != nil {
			logger.Warnf("error piping content through gzip: %v", err)
		}
	}()

	clean.success = true
	return r, clean.cleanup, nil
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

func (dd *DockerDiscoverer) Containers(ctx context.Context) ([]models.ContainerInfo, error) {
	// List all docker containers
	containers, err := dd.client.ContainerList(ctx, dtypes.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := make([]models.ContainerInfo, len(containers))
	for i, container := range containers {
		// Get container info
		info, err := dd.getContainerInfo(ctx, container.ID, container.ImageID)
		if err != nil {
			return nil, fmt.Errorf("failed to convert container to ContainerInfo: %w", err)
		}
		result[i] = info
	}
	return result, nil
}

func (dd *DockerDiscoverer) getContainerInfo(ctx context.Context, containerID, imageID string) (models.ContainerInfo, error) {
	// Inspect container
	info, err := dd.client.ContainerInspect(ctx, containerID)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("failed to inspect container: %w", err)
	}

	createdAt, err := time.Parse(time.RFC3339, info.Created)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("failed to parse time: %w", err)
	}

	// Get container image info
	imageInfo, err := dd.getContainerImageInfo(ctx, imageID)
	if err != nil {
		return models.ContainerInfo{}, err
	}

	return models.ContainerInfo{
		ContainerName: utils.PointerTo(info.Name),
		CreatedAt:     utils.PointerTo(createdAt),
		ContainerID:   containerID,
		Image:         utils.PointerTo(imageInfo),
		Labels:        convertTags(info.Config.Labels),
		ObjectType:    "ContainerInfo",
	}, nil
}
