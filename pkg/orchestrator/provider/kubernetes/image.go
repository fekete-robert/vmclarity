package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/containerruntimediscovery"
)

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType) error {
	imageDiscovererLabels := map[string]string{
		"app.kubernetes.io/component": "cr-discovery-server",
		"app.kubernetes.io/name":      "vmclarity",
	}
	// TODO(sambetts) Make namespace configurable
	discoverers, err := p.clientset.CoreV1().Pods("vmclarity").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(imageDiscovererLabels).String(),
	})
	if err != nil {
		return fmt.Errorf("unable to list image discoverers: %w", err)
	}

	for _, discoverer := range discoverers.Items {
		resp, err := http.Get(fmt.Sprintf("http://%s:8080/images", discoverer.Status.PodIP))
		if err != nil {
			return fmt.Errorf("unable to contact discoverer %s: %w", discoverer.Name, err)
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("unexpected error from discoverer %s, status %s", discoverer.Name, err)
		}

		var imageResponse containerruntimediscovery.ListImagesResponse
		decoder := json.NewDecoder(resp.Body)
		err = decoder.Decode(&imageResponse)
		if err != nil {
			return fmt.Errorf("unable to decode response from discoverer %s: %w", discoverer.Name, err)
		}

		for _, image := range imageResponse.Images {
			// Convert to asset
			asset := models.AssetType{}
			err = asset.FromContainerImageInfo(image)
			if err != nil {
				return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
			}

			outputChan <- asset
		}
	}

	return nil
}
