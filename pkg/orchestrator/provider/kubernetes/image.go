package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/containerruntimediscovery"
)

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType) error {
	// TODO(sambetts) Make namespace configurable
	discoverers, err := p.clientset.CoreV1().Pods("vmclarity").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(crDiscovererLabels).String(),
	})
	if err != nil {
		return fmt.Errorf("unable to list discoverers: %w", err)
	}

	for _, discoverer := range discoverers.Items {
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
