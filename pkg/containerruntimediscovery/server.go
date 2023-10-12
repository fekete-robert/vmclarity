package containerruntimediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/openclarity/vmclarity/api/models"
)

type ListImagesResponse struct {
	Images []models.ContainerImageInfo
}

type ListContainersResponse struct {
	Containers []models.ContainerInfo
}

type ContainerRuntimeDiscoveryServer struct {
	logger *logrus.Entry
	server *http.Server

	discoverer Discoverer
}

func NewContainerRuntimeDiscoveryServer(logger *logrus.Entry, listenAddr string, discoverer Discoverer) *ContainerRuntimeDiscoveryServer {
	ids := &ContainerRuntimeDiscoveryServer{
		discoverer: discoverer,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/images", ids.ListImages)
	mux.HandleFunc("/containers", ids.ListContainers)

	ids.server = &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,  // nolint:gomnd
		IdleTimeout:       30 * time.Second, // nolint:gomnd
	}

	return ids
}

func (ids *ContainerRuntimeDiscoveryServer) Serve() {
	go func() {
		if err := ids.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			ids.logger.Fatalf("image resolver server error: %v", err)
		}
	}()
}

func (ids *ContainerRuntimeDiscoveryServer) Shutdown(ctx context.Context) error {
	err := ids.server.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("failed to shutdown server: %w", err)
	}
	return nil
}

func (ids *ContainerRuntimeDiscoveryServer) ListImages(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/images" {
		http.NotFound(w, req)
		return
	}

	if req.Method != http.MethodGet {
		http.Error(w, fmt.Sprintf("400 unsupported method %v", req.Method), http.StatusBadRequest)
		return
	}

	images, err := ids.discoverer.Images(req.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to discover images: %v", err), http.StatusInternalServerError)
		return
	}

	response := ListImagesResponse{
		Images: images,
	}
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	err = encoder.Encode(response)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}

func (ids *ContainerRuntimeDiscoveryServer) ListContainers(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/containers" {
		http.NotFound(w, req)
		return
	}

	if req.Method != http.MethodGet {
		http.Error(w, fmt.Sprintf("400 unsupported method %v", req.Method), http.StatusBadRequest)
		return
	}

	containers, err := ids.discoverer.Containers(req.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to discover containers: %v", err), http.StatusInternalServerError)
		return
	}

	response := ListContainersResponse{
		Containers: containers,
	}
	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	err = encoder.Encode(response)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}
