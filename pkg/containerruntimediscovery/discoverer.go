package containerruntimediscovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/shared/log"
)

type Discoverer interface {
	Images(ctx context.Context) ([]models.ContainerImageInfo, error)
	Containers(ctx context.Context) ([]models.ContainerInfo, error)
}

type DiscovererFactory func(ctx context.Context) (Discoverer, error)

var discovererFactories map[string]DiscovererFactory = map[string]DiscovererFactory{
	"docker":     NewDockerDiscoverer,
	"containerd": NewContainerdDiscoverer,
}

// NewDiscoverer tries to create all registered discoverers and returns the
// first that succeeds, if none of them succeed then we return all the errors
// for the caller to evalulate.
func NewDiscoverer(ctx context.Context) (Discoverer, error) {
	logger := log.GetLoggerFromContextOrDefault(ctx)
	errs := []error{}

	for name, factory := range discovererFactories {
		discoverer, err := factory(ctx)
		if err == nil {
			logger.Infof("Loaded %s discoverer", name)
			return discoverer, nil
		}
		errs = append(errs, fmt.Errorf("failed to create %s discoverer: %w", name, err))
	}
	return nil, errors.Join(errs...)
}
