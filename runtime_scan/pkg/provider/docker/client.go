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

package docker

import (
	"context"
	"errors"
	"fmt"
	types "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"time"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/runtime_scan/pkg/provider"
	"github.com/openclarity/vmclarity/shared/pkg/utils"
	"sync"
)

type Client struct {
	dockerClient *client.Client
	config       *Config
}

func New(ctx context.Context) (*Client, error) {
	config, err := NewConfig()
	if err != nil {
		return nil, fmt.Errorf("invalid configuration. Provider=%s: %w", models.Docker, err)
	}

	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("failed to validate provider configuration. Provider=%s: %w", models.Docker, err)
	}

	dockerClient := &client.Client{}

	err = client.FromEnv(dockerClient)
	if err != nil {
		return nil, fmt.Errorf("failed to load provider configuration. Provider=%s: %w", models.Docker, err)
	}

	return &Client{
		dockerClient: dockerClient,
		config:       config,
	}, nil
}

func (c Client) Kind() models.CloudProvider {
	return models.Docker
}

func (c Client) DiscoverAssets(ctx context.Context) ([]models.AssetType, error) {
	var err error
	wg := &sync.WaitGroup{}
	errs := make(chan error, 2)
	assets := make(chan models.AssetType)

	wg.Add(1)
	go c.getImages(ctx, assets, errs, wg)

	wg.Add(1)
	go c.getContainers(ctx, assets, errs, wg)

	go func() {
		wg.Wait()
		close(errs)
		close(assets)
	}()

	var ret []models.AssetType
	for t := range assets {
		ret = append(ret, t)
	}

	for e := range errs {
		if e != nil {
			// nolint:typecheck
			err = errors.Join(err, e)
		}
	}
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (c Client) getImages(ctx context.Context, assets chan models.AssetType, errs chan error, wg *sync.WaitGroup) {
	defer wg.Done()

	images, err := c.dockerClient.ImageList(ctx, types.ImageListOptions{})
	if err != nil {
		errs <- provider.FatalError{
			Err: fmt.Errorf("failed to get images. Provider=%s: %w", models.Docker, err),
		}
		return
	}

	for _, i := range images {
		asset, err := c.getAssetFromImage(ctx, i.ID)
		if err != nil {
			errs <- provider.FatalError{
				Err: fmt.Errorf("failed to create AssetType from ContainerImageInfo. Provider=%s: %w", models.Docker, err),
			}
			return
		}
		assets <- asset
	}
}

func (c Client) getContainers(ctx context.Context, assets chan models.AssetType, errs chan error, wg *sync.WaitGroup) {
	defer wg.Done()

	containers, err := c.dockerClient.ContainerList(ctx, types.ContainerListOptions{
		All: true,
	})
	if err != nil {
		errs <- provider.FatalError{
			Err: fmt.Errorf("failed to get containers. Provider=%s: %w", models.Docker, err),
		}
		return
	}

	for _, container := range containers {
		inspect, err := c.dockerClient.ContainerInspect(ctx, container.ID)
		if err != nil {
			errs <- provider.FatalError{
				Err: fmt.Errorf("failed to get container. Provider=%s: %w", models.Docker, err),
			}
			return
		}

		created, err := time.Parse(time.RFC3339, inspect.Created)
		if err != nil {
			errs <- provider.FatalError{
				Err: fmt.Errorf("failed to parse time. Provider=%s: %w", models.Docker, err),
			}
			return
		}

		imageInfo, err := c.getContainerImageInfoFromImage(ctx, container.ImageID)
		if err != nil {
			// TODO (paralta) If image not required this should not be fatal
			errs <- provider.FatalError{
				Err: err,
			}
			return
		}

		asset := models.AssetType{}
		err = asset.FromContainerInfo(models.ContainerInfo{
			ContainerName: utils.PointerTo(inspect.Name),
			CreatedAt:     utils.PointerTo(created),
			Id:            utils.PointerTo(container.ID),
			Image:         utils.PointerTo(imageInfo),
			Labels:        convertTags(container.Labels),
			Location:      nil, // TODO (paralta) Clarify what is location
			ObjectType:    "ContainerInfo",
		})
		if err != nil {
			errs <- provider.FatalError{
				Err: fmt.Errorf("failed to create AssetType from ContainerInfo. Provider=%s: %w", models.Docker, err),
			}
			return
		}
		assets <- asset
	}
}

func (c Client) RunAssetScan(ctx context.Context, config *provider.ScanJobConfig) error {
	// TODO(paralta) Implement run scan for Docker Images
	containerInfo, err := config.AssetInfo.AsContainerInfo()
	if err != nil {
		return provider.FatalError{Err: err}
	}
	if containerInfo.ObjectType != "ContainerInfo" {
		return provider.FatalError{
			Err: fmt.Errorf("run target scan not implemented for current object type (%s). Provider=%s", models.Docker, containerInfo.ObjectType),
		}
	}

	volumeName := "scan_" + config.ScanID + "_container_" + *containerInfo.Id
	resp, err := c.dockerClient.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", volumeName)),
	})
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to get volumes. Provider=%s: %w", models.Docker, err),
		}
	}
	if len(resp.Volumes) == 0 {
		_, err = c.dockerClient.VolumeCreate(ctx, volume.CreateOptions{
			Name: volumeName,
		})
		if err != nil {
			return provider.FatalError{
				Err: fmt.Errorf("failed to create volume. Provider=%s: %w", models.Docker, err),
			}
		}
	}

	// TODO(paralta) Check if volume is empty before adding new content
	readCloser, err := c.dockerClient.ContainerExport(ctx, *containerInfo.Id)
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to export container. Provider=%s: %w", models.Docker, err),
		}
	}

	// Create an ephemeral container to populate volume with export output
	_, err = c.dockerClient.ImagePull(ctx, "alpine", types.ImagePullOptions{})
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to pull helper image. Provider=%s: %w", models.Docker, err),
		}
	}
	response, err := c.dockerClient.ContainerCreate(ctx,
		&container.Config{
			Image: "alpine",
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: volumeName,
					Target: "/data",
				},
			},
		}, nil, nil, "helper")
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to create helper container. Provider=%s: %w", models.Docker, err),
		}
	}
	defer func() {
		err = c.dockerClient.ContainerRemove(ctx, response.ID, types.ContainerRemoveOptions{})
		if err != nil {
			_ = fmt.Errorf("failed to remove helper container. Provider=%s: %w", models.Docker, err)
		}
	}()

	err = c.dockerClient.CopyToContainer(ctx, response.ID, "/data", readCloser, types.CopyToContainerOptions{})
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to copy data to container. Provider=%s: %w", models.Docker, err),
		}
	}

	// TODO(adamtagscherer) Get scanconfig.yaml into scan container
	// TODO(adamtagscherer) Start scan container

	return nil
}

func (c Client) RemoveAssetScan(ctx context.Context, config *provider.ScanJobConfig) error {

	containerInfo, err := config.AssetInfo.AsContainerInfo()
	if err != nil {
		return provider.FatalError{Err: err}
	}

	volumeName := "scan_" + config.ScanID + "_container_" + *containerInfo.Id
	err = c.dockerClient.VolumeRemove(ctx, volumeName, false)
	if err != nil {
		return provider.FatalError{
			Err: fmt.Errorf("failed to remove volume. Provider=%s: %w", models.Docker, err),
		}
	}
	return nil
}

func (c Client) getContainerImageInfoFromImage(ctx context.Context, id string) (models.ContainerImageInfo, error) {
	i, _, err := c.dockerClient.ImageInspectWithRaw(ctx, id)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to get image. Provider=%s: %w", models.Docker, err)
	}

	return models.ContainerImageInfo{
		Architecture: utils.PointerTo(i.Architecture),
		Id:           utils.PointerTo(i.ID),
		Labels:       convertTags(i.Config.Labels),
		ObjectType:   "ContainerImageInfo",
		Os:           utils.PointerTo(i.Os),
		Size:         utils.PointerTo(int(i.Size)),
	}, err
}

func (c Client) getAssetFromImage(ctx context.Context, id string) (models.AssetType, error) {
	asset := models.AssetType{}

	info, err := c.getContainerImageInfoFromImage(ctx, id)
	if err != nil {
		return asset, err
	}

	err = asset.FromContainerImageInfo(info)
	if err != nil {
		return asset, fmt.Errorf("failed to create AssetType from ContainerImageInfo. Provider=%s: %w", models.Docker, err)
	}

	return asset, err
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
