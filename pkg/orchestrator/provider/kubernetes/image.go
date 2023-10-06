package kubernetes

type ResolverResponseServer struct{
	server *http.Server
	response imageresolver.ResolverResponse
	err error
	done chan struct{}
}

func (rrs *ResolverResponseServer) Start() (string, chan struct{}) {
	rrs.done = make(chan struct{})

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return fmt.Errorf("failed to setup listener: %w", err)
	}

	rrs.server := &http.Server{Handler: rrs.handleResponse}
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warnf("Response server shutdown: %v", err)
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	ip := os.Getenv("DISCOVERY_LISTEN_IP")
	return fmt.Sprintf("http://%s:%d/", ip, port), rrs.done
}

func (rrs *ResolverResponseServer) handleResponse(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	err := decoder.Decode(&rrs.response)
	if err != nil {
		responseErr = err
	}
	close(rrs.done)
	w.WriteHeader(http.StatusOK)
}

func (rrs *ResolverResponseServer) Cleanup() error {
	err = server.Close()
	if err != nil {
		return fmt.Errorf("unable to shutdown the response server: %w", err)
	}
	rrs.server = nil
}

func (rrs *ResolverResponseServer) Wait(ctx context.Context) error {
	// Wait for the response or timeout
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
		imagesToResolve, imagePullSecrets, err := p.getImagesToResolveAndPullSecrets(ctx, namespace.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("unable to get images to resolve from namespace %s: %w", namespace.Name, err)
		}

		var response imageresolver.ResolverResponse
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

		config := imageresolver.ResolverConfig{
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

func (p *Provider) getImagesToResolveAndPullSecrets(ctx context.Context, namespaceName string) ([]imageresolver.ImageToResolve, []string, error) {
	imagesToResolve := map[imageresolver.ImageToResolve]struct{}{}
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
				Ref: ref,
				Arch: node.arch,
				Os: node.os,
			}] = struct{}{}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to iterate pods: %w", err)
	}

	return maps.Keys(imagesToResolve), maps.Keys(imagePullSecrets), nil
}
