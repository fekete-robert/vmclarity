package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	applyconfigurationscorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/pager"

	"github.com/openclarity/vmclarity/api/models"
	"github.com/openclarity/vmclarity/pkg/imageresolver"
	"github.com/openclarity/vmclarity/pkg/shared/log"
	"github.com/openclarity/vmclarity/pkg/shared/utils"
)

type ResolverResponseServer struct {
	server   *http.Server
	logger   *logrus.Entry
	response imageresolver.ResolverResponse
	err      error
	done     chan struct{}
}

func (rrs *ResolverResponseServer) Start(ctx context.Context) (string, error) {
	rrs.logger = log.GetLoggerFromContextOrDefault(ctx)
	rrs.done = make(chan struct{})

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", fmt.Errorf("failed to setup listener: %w", err)
	}

	rrs.server = &http.Server{Handler: rrs}
	go func() {
		err := rrs.server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			rrs.logger.Warnf("Response server shutdown: %v", err)
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	ip := os.Getenv("DISCOVERY_LISTEN_IP")
	return fmt.Sprintf("http://%s:%d/", ip, port), nil
}

func (rrs *ResolverResponseServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&rrs.response)
	if err != nil {
		rrs.logger.WithError(err).Errorf("failed to decode image resolver response")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	close(rrs.done)
	w.WriteHeader(http.StatusOK)
}

func (rrs *ResolverResponseServer) Cleanup() error {
	if rrs.server != nil {
		err := rrs.server.Close()
		if err != nil {
			return fmt.Errorf("unable to shutdown the response server: %w", err)
		}
		rrs.server = nil
	}
	return nil
}

func (rrs *ResolverResponseServer) Wait(ctx context.Context) error {
	// Wait for the response or timeout
	select {
	case <-rrs.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rrs *ResolverResponseServer) Response() imageresolver.ResolverResponse {
	return rrs.response
}

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType) error {
	nodes, err := p.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}

	nodeMap := map[string]nodeWithPlatform{}

	for _, node := range nodes.Items {
		nodeMap[node.Name] = nodeWithPlatform{
			arch: node.Status.NodeInfo.Architecture,
			os:   node.Status.NodeInfo.OperatingSystem,
		}
	}

	namespaces, err := p.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("unable to list namespaces: %w", err)
	}

	var errs []error
	for _, namespace := range namespaces.Items {
		err := p.discoverImagesForNamespace(ctx, namespace.Name, nodeMap, outputChan)
		if err != nil {
			errs = append(errs, fmt.Errorf("error discovering images from namespace %s: %w", namespace.Name, err))
		}
	}
	err = errors.Join(errs...)
	if err != nil {
		return fmt.Errorf("errors occured while discovering images from namespaces: %w", err)
	}

	return nil
}

func (p *Provider) discoverImagesForNamespace(ctx context.Context, namespace string, nodeMap map[string]nodeWithPlatform, outputChan chan models.AssetType) error {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	imagesToResolve, imagePullSecrets, err := p.getImagesToResolveAndPullSecrets(ctx, namespace, nodeMap)
	if err != nil {
		return fmt.Errorf("unable to get images to resolve from namespace %s: %w", namespace, err)
	}

	// Setup and start the resolver response server
	rrs := ResolverResponseServer{}
	rrsAddr, err := rrs.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start image resolver response server: %w", err)
	}
	defer func() {
		err := rrs.Cleanup()
		if err != nil {
			logger.WithError(err).Errorf("failed to cleanup image resolver response server")
		}
	}()

	// Generate the configuration for the resolver
	config := imageresolver.ResolverConfig{
		ResultEndpoint:  rrsAddr,
		ImagesToResolve: imagesToResolve,
	}
	if len(imagePullSecrets) > 0 {
		config.ImagePullSecretPath = imagePullSecretMountPath
	}

	// Run the resolver kubernetes job
	err = p.runImageResolverJob(ctx, config, namespace, imagePullSecrets)
	if err != nil {
		return fmt.Errorf("failed to run image resolver job: %w", err)
	}

	// Wait for the response or timeout
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err = rrs.Wait(ctx)
	if err != nil {
		return fmt.Errorf("error waiting for response from resolver: %w", err)
	}

	response := rrs.Response()

	for _, image := range response.Images {
		// Convert to asset
		asset := models.AssetType{}
		err = asset.FromContainerImageInfo(image)
		if err != nil {
			return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
		}

		outputChan <- asset
	}

	if len(response.Errors) > 0 {
		return fmt.Errorf("error(s) while resolving images: %v", response.Errors)
	}

	return nil
}

func (p *Provider) getImagesToResolveAndPullSecrets(ctx context.Context, namespace string, nodeMap map[string]nodeWithPlatform) ([]imageresolver.ImageToResolve, []string, error) {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	imagesToResolve := map[imageresolver.ImageToResolve]struct{}{}
	imagePullSecrets := map[string]struct{}{}

	pager := pager.New(func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
		// nolint:wrapcheck
		return p.clientset.CoreV1().Pods(namespace).List(ctx, opts)
	})
	err := pager.EachListItem(ctx, metav1.ListOptions{}, func(obj runtime.Object) error {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			// For some reason we didn't get a pod not sure why
			// lets continue.
			logger.Warnf("unexpected object type %T while handling pod", obj)
			return nil
		}

		for _, pullSecret := range pod.Spec.ImagePullSecrets {
			imagePullSecrets[pullSecret.Name] = struct{}{}
		}

		for _, containerStatus := range pod.Status.ContainerStatuses {
			ref := containerStatus.ImageID
			if strings.HasPrefix(ref, "docker://") || strings.HasPrefix(ref, "sha256") {
				// Ignore local references for now
				continue
			}
			ref = strings.TrimPrefix(ref, "docker-pullable://")

			node, ok := nodeMap[pod.Spec.NodeName]
			if !ok {
				// Without the node we can't resolve
				// the image as we don't know the arch
				// or os.
				continue
			}

			imagesToResolve[imageresolver.ImageToResolve{
				Ref:  ref,
				Arch: node.arch,
				Os:   node.os,
			}] = struct{}{}
		}

		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to iterate pods: %w", err)
	}

	return maps.Keys(imagesToResolve), maps.Keys(imagePullSecrets), nil
}

func (p *Provider) runImageResolverJob(ctx context.Context, config imageresolver.ResolverConfig, namespace string, imagePullSecrets []string) error {
	configBytes, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config to yaml: %w", err)
	}
	configMap := applyconfigurationscorev1.ConfigMap("vmclarity-discovery", namespace)
	configMap.BinaryData = map[string][]byte{
		"config.yaml": configBytes,
	}
	_, err = p.clientset.CoreV1().ConfigMaps(namespace).Apply(ctx, configMap, metav1.ApplyOptions{
		FieldManager: "vmclarity",
	})
	if err != nil {
		return fmt.Errorf("failed to create or update config map: %w", err)
	}

	jobName := fmt.Sprintf("vmclarity-discovery-%s", uuid.New().String())
	var backOffLimit int32 = 0
	image := "docker.io/tehsmash/vmclarity-k8s-image-resolver:k8simagediscovery"
	jobSpec := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: utils.PointerTo(int32(120)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            jobName,
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Args: []string{
								"--config",
								"/etc/vmclarity/config.yaml",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									ReadOnly:  true,
									MountPath: "/etc/vmclarity",
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "vmclarity-discovery",
									},
								},
							},
						},
					},
				},
			},
			BackoffLimit: &backOffLimit,
		},
	}

	for _, secretName := range imagePullSecrets {
		addJobImagePullSecretVolume(jobSpec, secretName)
	}

	_, err = p.clientset.BatchV1().Jobs(namespace).Create(ctx, jobSpec, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("unable to create job: %w", err)
	}

	return nil
}
