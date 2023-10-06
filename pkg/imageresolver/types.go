package imageresolver

import "github.com/openclarity/vmclarity/api/models"

type ImageToResolve struct {
	Ref  string `yaml:"ref"`
	Os   string `yaml:"os"`
	Arch string `yaml:"arch"`
}

type ResolverConfig struct {
	ImagesToResolve     []ImageToResolve `yaml:"imagesToResolve"`
	ImagePullSecretPath string           `yaml:"imagePullSecretPath"`
	ResultEndpoint      string           `yaml:"resultEndpoint"`
}

type ResolverResponse struct {
	Images []models.ContainerImageInfo
	Errors []string
}
