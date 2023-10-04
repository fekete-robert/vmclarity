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
	"os"
	"time"
	"context"
	"errors"
	"fmt"
	"strings"
	"path"
	"net/http"
	"net"
	"encoding/json"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchv1 "k8s.io/api/batch/v1"
	applyconfigurationscorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	//applyconfigurationsmetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/pager"
	"github.com/google/uuid"

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

type nodeWithPlatform struct {
	arch string
	os string
}

type ImageToResolve struct {
	Ref string `yaml:"ref"`
	Os string `yaml:"os"`
	Arch string `yaml:"arch"`
}

type ResolverConfig struct {
	ImagesToResolve []ImageToResolve `yaml:"imagesToResolve"`
	ImagePullSecretPath string `yaml:"imagePullSecretPath"`
	ResultEndpoint string `yaml:"resultEndpoint"`
}

type ResolverResponse struct {
	Images []models.ContainerImageInfo
	Errors []string
}

// nolint:cyclop,gocognit
func (p *Provider) discoverImages(ctx context.Context, outputChan chan models.AssetType) error {
	logger := log.GetLoggerFromContextOrDefault(ctx)

	nodes, err := p.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to get nodes: %w", err)
	}
	
	nodeMap := map[string]nodeWithPlatform{}

	for _, node := range nodes.Items {
		nodeMap[node.Name] = nodeWithPlatform{
			arch: node.Status.NodeInfo.Architecture,
			os: node.Status.NodeInfo.OperatingSystem,
		}
	}

	namespaces, err := p.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("unable to list namespaces: %w", err)
	}

	var errs []error
	for _, namespace := range namespaces.Items {
		imagesToResolve := map[ImageToResolve]struct{}{}
		imagePullSecrets := map[string]struct{}{}

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

				imagesToResolve[ImageToResolve{
					Ref: ref,
					Arch: node.arch,
					Os: node.os,
				}] = struct{}{}
			}

			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to iterate pods for namespace: %w", err))
			continue
		}

		var response ResolverResponse
		done := make(chan struct{})
		var responseErr error
		handlerFunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(done)
			decoder := json.NewDecoder(r.Body)
			err := decoder.Decode(&response)
			if err != nil {
				responseErr = err
			}
			w.WriteHeader(http.StatusOK)
			return
		})

		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to setup listener: %w", err))
			continue
		}
		server := &http.Server{Handler: handlerFunc}
		go func() {
			err := server.Serve(listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Warnf("Response server shutdown: %v", err)
			}
		}()

		port := listener.Addr().(*net.TCPAddr).Port
		ip := os.Getenv("DISCOVERY_LISTEN_IP")

		config := ResolverConfig{
			ResultEndpoint: fmt.Sprintf("http://%s:%d/", ip, port),
		}
		for imageToResolve := range imagesToResolve {
			config.ImagesToResolve = append(config.ImagesToResolve, imageToResolve)
		}

		if len(imagePullSecrets) > 0 {
			config.ImagePullSecretPath = imagePullSecretMountPath
		}

		configBytes, err := yaml.Marshal(config)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to marshal config to yaml: %w", err))
			continue
		}

		configMap := applyconfigurationscorev1.ConfigMap("vmclarity-discovery", namespace.Name)
		configMap.BinaryData = map[string][]byte{
			"config.yaml": configBytes,
		}
		_, err = p.clientset.CoreV1().ConfigMaps(namespace.Name).Apply(ctx, configMap, metav1.ApplyOptions{
			FieldManager: "vmclarity",
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to create config map: %w", err))
			continue
		}

		jobName := fmt.Sprintf("vmclarity-discovery-%s", uuid.New().String())
		var backOffLimit int32 = 0
		image := "docker.io/tehsmash/vmclarity-k8s-image-resolver:k8simagediscovery"
		jobSpec := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: namespace.Name,
			},
			Spec: batchv1.JobSpec{
				TTLSecondsAfterFinished: utils.PointerTo(int32(120)),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:    jobName,
								Image:   image,
								ImagePullPolicy: corev1.PullAlways,
								Args: []string{
									"--config",
									"/etc/vmclarity/config.yaml",
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name: "config",
										ReadOnly: true,
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

		for secretName := range imagePullSecrets {
			addJobImagePullSecretVolume(jobSpec, secretName)
		}

		_, err = p.clientset.BatchV1().Jobs(namespace.Name).Create(ctx, jobSpec, metav1.CreateOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("unable to create job: %w", err))
			continue
		}

		// Wait for the response or timeout
		select {
		case <-done:
		case <-time.After(2 * time.Minute):
			responseErr = fmt.Errorf("Timed out waiting for response from namespace")
		}

		err = server.Close()
		if err != nil {
			errs = append(errs, fmt.Errorf("unable to shutdown the response server: %w", err))
			continue
		}

		if responseErr != nil {
			errs = append(errs, fmt.Errorf("failed to process response: %w", responseErr))
			continue
		} else {
			logger.Infof("Image resolving is completed for namespace %s", namespace.Name)

			for _, image := range response.Images {
				// Convert to asset
				asset := models.AssetType{}
				err = asset.FromContainerImageInfo(image)
				if err != nil {
					return fmt.Errorf("failed to create AssetType from ContainerImageInfo: %w", err)
				}

				outputChan <- asset
			}

			for _, err := range response.Errors {
				logger.Errorf("error resolving image in namespace %s: %v", namespace.Name, err)
			}
		}
	}
	err = errors.Join(errs...)
	if err != nil {
		return fmt.Errorf("failed to iterate all pods: %w", err)
	}

	return nil
}

const (
	imagePullSecretMountPath    = "/opt/vmclarity-pull-secrets" // nolint:gosec
	imagePullSecretVolumePrefix = "image-pull-secret-"            // nolint:gosec
)

// Mount image pull secret as a volume into /opt/kubeclarity-pull-secrets so
// that the scanner job can find it. setJobImagePullSecretPath must be used in
// addition to this function to configure IMAGE_PULL_SECRET_PATH environment
// variable.
//  1. Create a volume "image-pull-secret-secretName" that holds the
//     `secretName` data. We don't know if this secret exists so mark it
//     optional so it doesn't block the pod starting.
//  2. Mount the volume into each container to a specific path
//     /opt/kubeclarity-pull-secrets/secretName
func addJobImagePullSecretVolume(job *batchv1.Job, secretName string) {
	volumeName := fmt.Sprintf("%s%s", imagePullSecretVolumePrefix, secretName)
	optional := true
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName: secretName,
				Optional:   &optional,
			},
		},
	})
	for i := range job.Spec.Template.Spec.Containers {
		container := &job.Spec.Template.Spec.Containers[i]
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      volumeName,
			ReadOnly:  true,
			MountPath: path.Join(imagePullSecretMountPath, secretName),
		})
	}
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
					return fmt.Errorf("failed to create AssetType from ContainerInfo: %w", err)
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
