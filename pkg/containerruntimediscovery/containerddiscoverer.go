package containerruntimediscovery

import (
	"context"
	"fmt"

	"github.com/containerd/containerd"
	"github.com/containerd/nerdctl/pkg/imgutil"
	"github.com/containerd/nerdctl/pkg/labels/k8slabels"
	"github.com/containers/image/v5/docker/reference"
	"github.com/sirupsen/logrus"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type ContainerdDiscoverer struct {
	client *containerd.Client
	logger *logrus.Entry
}

func NewContainerdDiscoverer(ctx context.Context) (Discoverer, error) {
	client, err := containerd.New("/var/run/containerd/containerd.sock", containerd.WithDefaultNamespace("k8s.io"))
	if err != nil {
		return nil, fmt.Errorf("failed to create containerd client: %w", err)
	}

	_, err = client.ListImages(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	return &ContainerdDiscoverer{
		client: client,
		logger: log.GetLoggerFromContextOrDefault(ctx),
	}, nil
}

func (cd *ContainerdDiscoverer) Images(ctx context.Context) ([]models.ContainerImageInfo, error) {
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

		existing, ok := imageSet[*cii.Id]
		if ok {
			merged, err := models.MergeContainerImage(existing, cii)
			if err != nil {
				return nil, fmt.Errorf("unable to merge image %v with %v: %w", existing, cii, err)
			}
			cii = merged
		}
		imageSet[*cii.Id] = cii
	}

	result := []models.ContainerImageInfo{}
	for _, image := range imageSet {
		result = append(result, image)
	}
	return result, nil
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
		Id:           &id,
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

	imageInfo, err := cd.getContainerImageInfo(ctx, image)
	if err != nil {
		return models.ContainerInfo{}, fmt.Errorf("unable to convert image %s to container image info: %w", image.Name(), err)
	}

	return models.ContainerInfo{
		Id:            utils.PointerTo(container.ID()),
		ContainerName: utils.PointerTo(name),
		CreatedAt:     utils.PointerTo(createdAt),
		Image:         utils.PointerTo(imageInfo),
		Labels:        convertTags(labels),
		ObjectType:    "ContainerInfo",
	}, nil
}
