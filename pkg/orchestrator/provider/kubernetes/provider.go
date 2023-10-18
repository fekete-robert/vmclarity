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
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/orchestrator/provider"
	"github.com/openclarity/vmclarity/pkg/shared/backendclient"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type Provider struct {
	uuid          string
	clientset     kubernetes.Interface
	config        *Config
	backendClient *backendclient.BackendClient
}

var _ provider.Provider = &Provider{}

func New(ctx context.Context, b *backendclient.BackendClient) (provider.Provider, error) {
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
		clientset:     clientset,
		config:        config,
		backendClient: b,
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
	assetDiscoverer.Error = fmt.Errorf("not implemented")
	close(assetDiscoverer.OutputChan)
	return assetDiscoverer
}

func (p *Provider) RunAssetScan(context.Context, *provider.ScanJobConfig) error {
	return fmt.Errorf("not implemented")
}

func (p *Provider) RemoveAssetScan(context.Context, *provider.ScanJobConfig) error {
	return fmt.Errorf("not implemented")
}

func (p *Provider) Register(ctx context.Context) error {
	// TODO(paralta) When persistent storage is available, check if the provider is already registered.
	// If not registered, post the provider and store the received UUID.
	apiProvider, err := p.backendClient.PostProvider(
		ctx,
		models.Provider{
			DisplayName: utils.PointerTo(string(p.Kind())),
			Status: &models.ProviderStatus{
				State:              models.ProviderStatusStateUnknown,
				Reason:             models.NoHeartbeatReceived,
				LastTransitionTime: time.Now(),
			},
		})
	if err != nil {
		return fmt.Errorf("failed to post provider: %w", err)
	}

	p.uuid = *apiProvider.Id
	return nil
}
