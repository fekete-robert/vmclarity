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
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/pkg/cleanup"
	criConstants "github.com/containerd/containerd/pkg/cri/constants"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/nerdctl/pkg/imgutil"
	"github.com/containerd/nerdctl/pkg/labels/k8slabels"
	"github.com/containers/image/v5/docker/reference"
	"github.com/containerd/containerd/mount"
	"github.com/opencontainers/image-spec/identity"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type ContainerdDiscoverer struct {
	client *containerd.Client
}

func NewContainerdDiscoverer(ctx context.Context) (Discoverer, error) {
	// Containerd supports multiple namespaces so that a single daemon can
	// be used by multiple clients like Docker and Kubernetes and the
	// resources will not conflict etc. In order to discover all the
	// containers for kubernetes we need to set the kubernetes namespace as
	// the default for our client.
	client, err := containerd.New("/var/run/containerd/containerd.sock", containerd.WithDefaultNamespace(criConstants.K8sContainerdNamespace))
	if err != nil {
		return nil, fmt.Errorf("failed to create containerd client: %w", err)
	}

	_, err = client.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	return &ContainerdDiscoverer{
		client: client,
	}, nil
}

func (cd *ContainerdDiscoverer) Images(ctx context.Context) ([]models.ContainerImageInfo, error) {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	images, err := cd.client.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	imageSet := map[string]models.ContainerImageInfo{}
	for _, image := range images {
		cii, err := cd.getContainerImageInfo(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("unable to convert image %s to container image info: %w", image.Name(), err)
		}

		if cii.ImageID == "" {
			logger.Warnf("found image with empty ImageID: %s", cii.String())
			continue
		}

		existing, ok := imageSet[cii.ImageID]
		if ok {
			merged, err := existing.Merge(cii)
			if err != nil {
				return nil, fmt.Errorf("unable to merge image %v with %v: %w", existing, cii, err)
			}
			cii = merged
		}
		imageSet[cii.ImageID] = cii
	}

	result := []models.ContainerImageInfo{}
	for _, image := range imageSet {
		result = append(result, image)
	}
	return result, nil
}

func (cd *ContainerdDiscoverer) Image(ctx context.Context, imageID string) (models.ContainerImageInfo, error) {
	// ContainerD doesn't allow to filter images by config digest, so we
	// have to walk all the images to find all the images by ID and then
	// merge them together.
	images, err := cd.client.ListImages(ctx)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to list images: %w", err)
	}

	var result models.ContainerImageInfo
	var found bool
	for _, image := range images {
		configDescriptor, err := image.Config(ctx)
		if err != nil {
			return models.ContainerImageInfo{}, fmt.Errorf("failed to load image config descriptor: %w", err)
		}
		id := configDescriptor.Digest.String()

		if id != imageID {
			continue
		}

		found = true

		cii, err := cd.getContainerImageInfo(ctx, image)
		if err != nil {
			return models.ContainerImageInfo{}, fmt.Errorf("unable to convert image %s to container image info: %w", image.Name(), err)
		}

		result, err = result.Merge(cii)
		if err != nil {
			return models.ContainerImageInfo{}, fmt.Errorf("unable to merge image %v with %v: %w", result, cii, err)
		}
	}
	if !found {
		return models.ContainerImageInfo{}, ErrNotFound
	}

	return result, nil
}

// TODO(sambetts) Support auth config for fetching private images if they are missing
func (cd *ContainerdDiscoverer) ExportImage(ctx context.Context, imageID string, output io.Writer) error {
	imageInfo, err := cd.Image(ctx, imageID)
	if err != nil {
		return fmt.Errorf("failed to get image info to export: %w", err)
	}

	if imageInfo.RepoDigests == nil || len(*imageInfo.RepoDigests) == 0 {
		return fmt.Errorf("image has no known repo digests can not safely fetch it: %w", err)
	}

	ctx, done, err := cd.client.WithLease(ctx, leases.WithRandomID(), leases.WithExpiration(1*time.Hour))
	if err != nil {
		return fmt.Errorf("failed to get lease from containerd: %w", err)
	}
	defer done(ctx)

	// NOTE(sambetts) When running in Kubernetes containerd can be
	// configured to garbage collect the un-expanded blobs from the content
	// store after they are converted to a rootfs snapshot that is used to
	// boot containers. For this reason we need to re-fetch the image to
	// ensure that all the required blobs for export are in the content
	// store.
	//
	// TODO(sambetts) Maybe try all the digests in case one has gone missing?
	ref := (*imageInfo.RepoDigests)[0]
	img, err := cd.client.Fetch(ctx, ref)
	if err != nil {
		return fmt.Errorf("failed to fetch image %s: %w", ref, err)
	}

	return cd.client.Export(
		ctx,
		output,
		archive.WithImage(cd.client.ImageService(), img.Name),
		archive.WithPlatform(platforms.All),
	)
}

type cleanuper struct {
	cleanups []func()
	success bool
}

func (c *cleanuper) add(cleanup func()) {
	c.cleanups = append(c.cleanups, cleanup)
}

func (c *cleanuper) ifNotSuccessful() {
	if !c.success {
		c.cleanup()
	}
}

func (c *cleanuper) cleanup() {
	for _, cleanup := range c.cleanups {
		cleanup()
	}
	c.cleanups = []func(){}
}

func (cd *ContainerdDiscoverer) ExportImageFilesystem(ctx context.Context, imageID string) (io.ReadCloser, func(), error) {
	clean := &cleanuper{}
	defer clean.ifNotSuccessful()

	images, err := cd.client.ListImages(ctx)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to list images: %w", err)
	}

	var i containerd.Image
	var found bool
	for _, image := range images {
		configDescriptor, err := image.Config(ctx)
		if err != nil {
			return nil, clean.cleanup, fmt.Errorf("failed to load image config descriptor: %w", err)
		}
		id := configDescriptor.Digest.String()

		if id == imageID {
			i = image
			found = true
			break
		}
	}
	if !found {
		return nil, clean.cleanup, fmt.Errorf("no image with ID %s found to export", imageID)
	}

	diffIDs, err := i.RootFS(ctx)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to get diff IDs from image config: %w", err)
	}

	ctx, done, err := cd.client.WithLease(ctx, leases.WithRandomID(), leases.WithExpiration(1*time.Hour))
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to get lease from containerd: %w", err)
	}
	defer done(ctx)

	snapshotter := containerd.DefaultSnapshotter
	snSrv := cd.client.SnapshotService(snapshotter)
	cSrv := cd.client.ContentStore()
	compSrv := cd.client.DiffService()
	snID := identity.ChainID(diffIDs).String()
	mounts, err := snSrv.View(ctx, fmt.Sprintf("%s-export-view-%s", snID, uniquePart()), snID)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to get mounts from containerd: %w", err)
	}
	clean.add(func() {
		cleanup.Do(ctx, func(ctx context.Context) {
			snSrv.Remove(ctx, snID)
		})
	})

	desc, err := compSrv.Compare(ctx, []mount.Mount{}, mounts)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to generate content from snapshot: %w", err)
	}

	readerAt, err := cSrv.ReaderAt(ctx, desc)
	if err != nil {
		return nil, clean.cleanup, fmt.Errorf("failed to get reader for content: %w", err)
	}

	clean.success = true
	return io.NopCloser(content.NewReader(readerAt)), clean.cleanup, nil
}

func (cd *ContainerdDiscoverer) getContainerImageInfo(ctx context.Context, image containerd.Image) (models.ContainerImageInfo, error) {
	configDescriptor, err := image.Config(ctx)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to load image config descriptor: %w", err)
	}
	id := configDescriptor.Digest.String()

	imageSpec, err := image.Spec(ctx)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to load image spec: %w", err)
	}

	// NOTE(sambetts) We can not use image.Size as it gives us the size of
	// the compressed layers and not the real size of the content.
	snapshotter := cd.client.SnapshotService(containerd.DefaultSnapshotter)
	size, err := imgutil.UnpackedImageSize(ctx, snapshotter, image)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("unable to determine size for image: %w", err)
	}

	repoTags, repoDigests := ParseImageReferences([]string{image.Name()})

	return models.ContainerImageInfo{
		ImageID:      id,
		Architecture: utils.PointerTo(imageSpec.Architecture),
		Labels:       convertTags(imageSpec.Config.Labels),
		RepoTags:     &repoTags,
		RepoDigests:  &repoDigests,
		ObjectType:   "ContainerImageInfo",
		Os:           utils.PointerTo(imageSpec.OS),
		Size:         utils.PointerTo(int(size)),
	}, nil
}

// ParseImageReferences parses a list of arbitrary image references and returns
// the repotags and repodigests.
func ParseImageReferences(refs []string) ([]string, []string) {
	var tags, digests []string
	for _, ref := range refs {
		parsed, err := reference.ParseAnyReference(ref)
		if err != nil {
			continue
		}
		if _, ok := parsed.(reference.Canonical); ok {
			digests = append(digests, parsed.String())
		} else if _, ok := parsed.(reference.Tagged); ok {
			tags = append(tags, parsed.String())
		}
	}
	return tags, digests
}

func (cd *ContainerdDiscoverer) Containers(ctx context.Context) ([]models.ContainerInfo, error) {
	containers, err := cd.client.Containers(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to list containers: %w", err)
	}

	result := make([]models.ContainerInfo, len(containers))
	for i, container := range containers {
		// Get container info
		info, err := cd.getContainerInfo(ctx, container)
		if err != nil {
			return nil, fmt.Errorf("failed to convert container to ContainerInfo: %w", err)
		}
		result[i] = info
	}
	return result, nil
}

func (cd *ContainerdDiscoverer) Container(ctx context.Context, containerID string) (models.ContainerInfo, error) {
	containerSvc := cd.client.ContainerService()
	container, err := containerSvc.Get(ctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("failed to get container from store: %w", err)
	}

	return cd.getContainerInfo(ctx, container)
}

func (cd *ContainerdDiscoverer) ExportContainer(ctx context.Context, containerID string) (io.ReadCloser, func(), error) {
	return nil, func(){}, fmt.Errorf("Not Implemented")
}

func (cd *ContainerdDiscoverer) getContainerInfo(ctx context.Context, container containerd.Container) (models.ContainerInfo, error) {
	id := container.ID()

	labels, err := container.Labels(ctx)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("unable to get labels for container %s: %w", id, err)
	}
	// If this doesn't exist then use empty string as the name. Containerd
	// doesn't have the concept of a Name natively.
	name := labels[k8slabels.ContainerName]

	info, err := container.Info(ctx)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("unable to get info for container %s: %w", id, err)
	}
	createdAt := info.CreatedAt

	image, err := container.Image(ctx)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("unable to get image from container %s: %w", id, err)
	}

	configDescriptor, err := image.Config(ctx)
	if err != nil {
		return models.ContainerImageInfo{}, fmt.Errorf("failed to load image config descriptor: %w", err)
	}
	imageID := configDescriptor.Digest.String()

	imageInfo, err := cd.Image(ctx, imageID)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("unable to convert image %s to container image info: %w", image.Name(), err)
	}

	return models.ContainerInfo{
		ContainerID:   container.ID(),
		ContainerName: utils.PointerTo(name),
		CreatedAt:     utils.PointerTo(createdAt),
		Image:         utils.PointerTo(imageInfo),
		Labels:        convertTags(labels),
		ObjectType:    "ContainerInfo",
	}, nil
}

func uniquePart() string {
	t := time.Now()
	var b [3]byte
	// Ignore read failures, just decreases uniqueness
	rand.Read(b[:])
	return fmt.Sprintf("%d-%s", t.Nanosecond(), base64.URLEncoding.EncodeToString(b[:]))
}
