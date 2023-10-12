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

import (
	"fmt"
	"strings"
)

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

func MergeComparable[T comparable](original, target T) (T, error) {
	var zero T
	if original != zero && target != zero && original != target {
		return zero, fmt.Errorf("%v does not match %v", original, target)
	}
	if original != zero {
		return original, nil
	}
	return target, nil
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

func MergeContainerImage(original, target ContainerImageInfo) (ContainerImageInfo, error) {
	id, err := MergeComparable(original.ImageID, target.ImageID)
	if err != nil {
		return original, fmt.Errorf("failed to merge Id field: %w", err)
	}

	size, err := MergeComparable(*original.Size, *target.Size)
	if err != nil {
		return original, fmt.Errorf("failed to merge Size field: %w", err)
	}

	os, err := MergeComparable(*original.Os, *target.Os)
	if err != nil {
		return original, fmt.Errorf("failed to merge Os field: %w", err)
	}

	architecture, err := MergeComparable(*original.Architecture, *target.Architecture)
	if err != nil {
		return original, fmt.Errorf("failed to merge Architecture field: %w", err)
	}

	labels := UnionSlices(*original.Labels, *target.Labels)

	repoDigests := UnionSlices(*original.RepoDigests, *target.RepoDigests)

	repoTags := UnionSlices(*original.RepoTags, *target.RepoTags)

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

const nilString = "nil"

func (cii ContainerImageInfo) String() string {
	size := nilString
	if cii.Size != nil {
		size = fmt.Sprintf("%d", *cii.Size)
	}

	labels := nilString
	if cii.Labels != nil {
		l := make([]string, len(*cii.Labels))
		for i, label := range *cii.Labels {
			l[i] = fmt.Sprintf("{Key: \"%s\", Value: \"%s\"}", label.Key, label.Value)
		}
		labels = fmt.Sprintf("[%s]", strings.Join(l, ", "))
	}

	os := nilString
	if cii.Os != nil {
		os = *cii.Os
	}

	architecture := nilString
	if cii.Architecture != nil {
		architecture = *cii.Architecture
	}

	repoDigests := nilString
	if cii.RepoDigests != nil {
		repoDigests = fmt.Sprintf("[%s]", strings.Join(*cii.RepoDigests, ", "))
	}

	repoTags := nilString
	if cii.RepoTags != nil {
		repoTags = fmt.Sprintf("[%s]", strings.Join(*cii.RepoTags, ", "))
	}

	return fmt.Sprintf("{ID: %s, Size: %s, Labels: %s, Arch: %s, OS: %s, Digests: %s, Tags: %s}", cii.ImageID, size, labels, architecture, os, repoDigests, repoTags)
}
