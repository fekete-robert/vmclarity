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
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/containerruntimediscovery"
)

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType, crDiscoverers []corev1.Pod) error {
	for _, discoverer := range crDiscoverers {
		err := p.discoverImagesFromDiscoverer(ctx, outputChan, discoverer)
		if err != nil {
			return fmt.Errorf("failed to discover images from discoverer %s: %w", discoverer.Name, err)
		}
	}

	return nil
}

func (p *Provider) discoverImagesFromDiscoverer(ctx context.Context, outputChan chan models.AssetType, discoverer corev1.Pod) error {
	discovererEndpoint := net.JoinHostPort(discoverer.Status.PodIP, "8080")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/images", discovererEndpoint), nil)
	if err != nil {
		return fmt.Errorf("unable to create request to discoverer: %w", err)
	}

	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("unable to contact discoverer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected error from discoverer status %s", resp.Status)
	}

	var imageResponse containerruntimediscovery.ListImagesResponse
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&imageResponse)
	if err != nil {
		return fmt.Errorf("unable to decode response from discoverer: %w", err)
	}

	resp.Body.Close()

	for _, image := range imageResponse.Images {
		// Convert to asset
		asset := models.AssetType{}
		err = asset.FromContainerImageInfo(image)
		if err != nil {
			return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
		}

		outputChan <- asset
	}

	return nil
}
