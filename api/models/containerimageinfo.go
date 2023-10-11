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

package models

import "fmt"
import "strings"

// GetFirstRepoTag returns the first repo tag if it exists. Otherwise, returns false.
func (c *ContainerImageInfo) GetFirstRepoTag() (string, bool) {
	var tag string
	var ok bool

	if c.RepoTags != nil && len(*c.RepoTags) > 0 {
		tag, ok = (*c.RepoTags)[0], true
	}

	return tag, ok
}

// GetFirstRepoDigest returns the first repo digest if it exists. Otherwise, returns false.
func (c *ContainerImageInfo) GetFirstRepoDigest() (string, bool) {
	var digest string
	var ok bool

	if c.RepoDigests != nil && len(*c.RepoDigests) > 0 {
		digest, ok = (*c.RepoDigests)[0], true
	}

	return digest, ok
}

func MergeComparable[T comparable](original, new T) (T, error) {
	var zero T
	if original != zero && new != zero && original != new {
		return zero, fmt.Errorf("%v does not match %v", original, new)
	}
	if original != zero {
		return original, nil
	}
	return new, nil
}

func UnionSlices[T comparable](inputs ...[]T) []T {
	seen := map[T]struct{}{}
	result := []T{}
	for _, i := range inputs {
		for _, j := range i {
			if _, ok := seen[j]; !ok {
				seen[j] = struct{}{}
				result = append(result, j)
			}
		}
	}
	return result
}

func MergeContainerImage(original, new ContainerImageInfo) (ContainerImageInfo, error) {
	id, err := MergeComparable(original.ImageID, new.ImageID)
	if err != nil {
		return original, fmt.Errorf("failed to merge Id field: %w", err)
	}

	size, err := MergeComparable(*original.Size, *new.Size)
	if err != nil {
		return original, fmt.Errorf("failed to merge Size field: %w", err)
	}

	os, err := MergeComparable(*original.Os, *new.Os)
	if err != nil {
		return original, fmt.Errorf("failed to merge Os field: %w", err)
	}

	architecture, err := MergeComparable(*original.Architecture, *new.Architecture)
	if err != nil {
		return original, fmt.Errorf("failed to merge Architecture field: %w", err)
	}

	labels := UnionSlices(*original.Labels, *new.Labels)

	repoDigests := UnionSlices(*original.RepoDigests, *new.RepoDigests)

	repoTags := UnionSlices(*original.RepoTags, *new.RepoTags)

	return ContainerImageInfo{
		ImageID:      id,
		Size:         &size,
		Labels:       &labels,
		Os:           &os,
		Architecture: &architecture,
		RepoDigests:  &repoDigests,
		RepoTags:     &repoTags,
	}, nil
}

func (cii ContainerImageInfo) String() string {
	size := "nil"
	if cii.Size != nil {
		size = fmt.Sprintf("%d", *cii.Size)
	}

	labels := "nil"
	if cii.Labels != nil {
		l := make([]string, len(*cii.Labels))
		for i, label := range *cii.Labels {
			l[i] = fmt.Sprintf("{Key: \"%s\", Value: \"%s\"}", label.Key, label.Value)
		}
		labels = fmt.Sprintf("[%s]", strings.Join(l, ", "))
	}

	os := "nil"
	if cii.Os != nil {
		os = *cii.Os
	}

	architecture := "nil"
	if cii.Architecture != nil {
		architecture = *cii.Architecture
	}

	repoDigests := "nil"
	if cii.RepoDigests != nil {
		repoDigests = fmt.Sprintf("[%s]", strings.Join(*cii.RepoDigests, ", "))
	}

	repoTags := "nil"
	if cii.RepoTags != nil {
		repoTags = fmt.Sprintf("[%s]", strings.Join(*cii.RepoTags, ", "))
	}

	return fmt.Sprintf("ID: %s, Size: %s, Labels: %s, Arch: %s, OS: %s, Digests: %s, Tags: %s", cii.ImageID, size, labels, architecture, os, repoDigests, repoTags)
}
