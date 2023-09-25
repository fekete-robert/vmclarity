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

package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/opencontainers/go-digest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/pager"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/orchestrator/provider"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type Provider struct {
	clientset kubernetes.Interface
	config    *Config
}

var _ provider.Provider = &Provider{}

func New(ctx context.Context) (provider.Provider, error) {
	config, err := NewConfig()
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	var clientConfig *rest.Config
	if config.KubeConfig == "" {
		// If KubeConfig config option not set, assume we're running
		// incluster.
		clientConfig, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to load in-cluster client configuration: %w", err)
		}
	} else {
		cc, err := clientcmd.LoadFromFile(config.KubeConfig)
		if err != nil {
			return nil, fmt.Errorf("unable to load kubeconfig from %s: %w", config.KubeConfig, err)
		}
		clientConfig, err = clientcmd.NewNonInteractiveClientConfig(*cc, "", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to create client configuration from the provided kubeconfig file: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create kubernetes clientset: %w", err)
	}

	return &Provider{
		clientset: clientset,
		config:    config,
	}, nil
}

func (p *Provider) Kind() models.CloudProvider {
	return models.Kubernetes
}

func (p *Provider) Estimate(ctx context.Context, stats models.AssetScanStats, asset *models.Asset, assetScanTemplate *models.AssetScanTemplate) (*models.Estimation, error) {
	return &models.Estimation{}, provider.FatalErrorf("Not Implemented")
}

func (p *Provider) DiscoverAssets(ctx context.Context) provider.AssetDiscoverer {
	assetDiscoverer := provider.NewSimpleAssetDiscoverer()

	go func() {
		defer close(assetDiscoverer.OutputChan)

		var errs []error

		err := p.discoverImages(ctx, assetDiscoverer.OutputChan)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to discover images: %w", err))
		}

		err = p.discoverContainers(ctx, assetDiscoverer.OutputChan)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to discover containers: %w", err))
		}

		assetDiscoverer.Error = errors.Join(errs...)
	}()

	return assetDiscoverer
}

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType) error {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	nodes, err := p.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}
	for _, node := range nodes.Items {
		for _, image := range node.Status.Images {
			// Because of the way docker images work its possible
			// that one image could have multiple digests if the
			// same image is uploaded to different repos which
			// generate different manifests.
			//
			// Kubernetes doesn't expose the ImageID which
			// collerates these manifest digests together, so for
			// now find all unique digests for this image, each of
			// these will be a unique asset.
			digests := map[digest.Digest][]reference.Canonical{}
			tags := map[string][]reference.NamedTagged{}
			for _, name := range image.Names {
				ref, err := reference.ParseAnyReference(name)
				if err != nil {
					logger.WithError(err).Errorf("failed to parse image reference %s", name)
				}
				switch v := ref.(type) {
				case reference.Canonical:
					digests[v.Digest()] = append(digests[v.Digest()], v)
				case reference.NamedTagged:
					tags[v.Name()] = append(tags[v.Name()], v)
				default:
					logger.Warnf("found unsupported image reference %s", ref.String())
				}
			}
			if len(digests) == 0 {
				logger.Warnf("found image with no digests, skipping...")
			}

			for digest, refs := range digests {
				nameSet := map[string]struct{}{}
				for _, ref := range refs {
					nameSet[ref.String()] = struct{}{}

					ts, ok := tags[ref.Name()]
					if !ok {
						continue
					}

					for _, t := range ts {
						nameSet[t.String()] = struct{}{}
					}
				}

				names := []string{}
				for name := range nameSet {
					names = append(names, name)
				}

				info := models.ContainerImageInfo{
					Id:           utils.PointerTo(digest.String()),
					Names:        &names,
					Size:         utils.PointerTo(int(image.SizeBytes)),
					Architecture: utils.PointerTo(node.Status.NodeInfo.Architecture),
					Os:           utils.PointerTo(node.Status.NodeInfo.OperatingSystem),
				}

				// Convert to asset
				asset := models.AssetType{}
				err = asset.FromContainerImageInfo(info)
				if err != nil {
					return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
				}

				outputChan <- asset
			}
		}
	}

	return nil
}

// nolint:cyclop
func (p *Provider) discoverContainers(ctx context.Context, outputChan chan models.AssetType) error {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	namespaces, err := p.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("unable to list namespaces: %w", err)
	}

	var errs []error
	for _, namespace := range namespaces.Items {
		pager := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			// nolint:wrapcheck
			return p.clientset.CoreV1().Pods(namespace.Name).List(ctx, opts)
		})
		err := pager.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				// For some reason we didn't get a pod not sure why
				// lets continue.
				logger.Warnf("unexpected object while iterating pods %T", obj)
				return nil
			}

			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.State.Running == nil {
					continue
				}

				// Container ID is prefixed with docker:// or
				// containerd:// so we'll remove them if present.
				containerID := containerStatus.ContainerID
				containerID, dFound := strings.CutPrefix(containerID, "docker://")
				containerID, cFound := strings.CutPrefix(containerID, "containerd://")
				if !(dFound || cFound) {
					logger.Warnf("unsupported containerID %s found, skipping...", containerID)
				}

				info := models.ContainerInfo{
					Id:            utils.PointerTo(containerID),
					ContainerName: utils.PointerTo(containerStatus.Name),
					Location:      utils.PointerTo(pod.Spec.NodeName),
					CreatedAt:     utils.PointerTo(containerStatus.State.Running.StartedAt.Time),
				}

				// Convert to asset
				asset := models.AssetType{}
				err = asset.FromContainerInfo(info)
				if err != nil {
					return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
				}

				outputChan <- asset
			}

			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to iterate pods for namespace: %w", err))
		}
	}
	err = errors.Join(errs...)
	if err != nil {
		return fmt.Errorf("failed to iterate all pods: %w", err)
	}

	return nil
}

func (p *Provider) RunAssetScan(context.Context, *provider.ScanJobConfig) error {
	return fmt.Errorf("not implemented")
}

func (p *Provider) RemoveAssetScan(context.Context, *provider.ScanJobConfig) error {
	return fmt.Errorf("not implemented")
}
