package imageresolver

import (
	"context"
	"fmt"
	"os"
	"path"

	corev1 "k8s.io/api/core/v1"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/authn/k8schain"

	"github.com/openclarity/vmclarity/pkg/shared/log"
)

func NewKeyChain(ctx context.Context, ipsPath string) (authn.Keychain, error) {
	if ipsPath == "" {
		keychain, err := k8schain.NewNoClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create no client keychain: %w", err)
		}

		return keychain, nil
	}

	secrets, err := readImagePullSecrets(ctx, ipsPath)
	if err != nil {
		return nil, fmt.Errorf("fail to read image pull secrets: %w", err)
	}

	keychain, err := k8schain.NewFromPullSecrets(ctx, secrets)
	if err != nil {
		return nil, fmt.Errorf("unable to load keychain from image pull secrets: %w", err)
	}

	return keychain, nil
}

func readImagePullSecrets(ctx context.Context, ipsPath string) ([]corev1.Secret, error) {
	secrets := []corev1.Secret{}
	files, err := os.ReadDir(ipsPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read path %s: %w", ipsPath, err)
	}

	for _, file := range files {
		// We expect directories for each secret in ipsPath
		if !file.IsDir() {
			continue
		}

		secretPath := path.Join(ipsPath, file.Name())
		secretType, secretDataKey, err := determineSecretTypeAndKey(secretPath)
		if err != nil {
			return nil, fmt.Errorf("unable to determine type of secret %s: %w", file.Name(), err)
		}

		if secretType != corev1.SecretTypeDockerConfigJson && secretType != corev1.SecretTypeDockercfg {
			log.GetLoggerFromContextOrDefault(ctx).Warnf("Secret %s is not a supported image pull secret type, ignoring.", file.Name())
			continue
		}

		secretFilePath := path.Join(ipsPath, file.Name(), secretDataKey)
		secretDataBytes, err := os.ReadFile(secretFilePath)
		if err != nil {
			return nil, fmt.Errorf("unable to read secret file %s: %w", secretFilePath, err)
		}

		secrets = append(secrets, corev1.Secret{
			Type: secretType,
			Data: map[string][]byte{
				secretDataKey: secretDataBytes,
			},
		})
	}

	return secrets, nil
}

func determineSecretTypeAndKey(secretPath string) (corev1.SecretType, string, error) {
	var unsetSecretType corev1.SecretType

	secretFiles, err := os.ReadDir(secretPath)
	if err != nil {
		return unsetSecretType, "", fmt.Errorf("unable to read secret directory %s: %w", secretPath, err)
	}

	for _, secretFile := range secretFiles {
		// We only want files at this point
		if secretFile.IsDir() {
			continue
		}

		switch secretFile.Name() {
		case corev1.DockerConfigJsonKey:
			return corev1.SecretTypeDockerConfigJson, corev1.DockerConfigJsonKey, nil
		case corev1.DockerConfigKey:
			return corev1.SecretTypeDockercfg, corev1.DockerConfigKey, nil
		}
	}

	return unsetSecretType, "", nil
}
