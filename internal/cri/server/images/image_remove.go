/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package images

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/tracing"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"

	ctrdlabels "github.com/containerd/containerd/v2/pkg/labels"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// RemoveImage removes the image.
// TODO(random-liu): Update CRI to pass image reference instead of ImageSpec. (See
// kubernetes/kubernetes#46255)
// TODO(random-liu): We should change CRI to distinguish image id and image spec.
// Remove the whole image no matter the it's image id or reference. This is the
// semantic defined in CRI now.
func (c *GRPCCRIImageService) RemoveImage(ctx context.Context, r *runtime.RemoveImageRequest) (*runtime.RemoveImageResponse, error) {
	err := c.CRIImageService.RemoveImage(ctx, r.GetImage())
	if err != nil && !errdefs.IsNotFound(err) {
		return nil, err
	}
	return &runtime.RemoveImageResponse{}, nil
}

func (c *CRIImageService) RemoveImage(ctx context.Context, imageSpec *runtime.ImageSpec) error {
	span := tracing.SpanFromContext(ctx)

	runtimeHandler := imageSpec.GetRuntimeHandler()
	platformSpec := platforms.DefaultSpec()
	if runtimeHandler != "" {
		if runtimePlatform, ok := c.runtimePlatforms[runtimeHandler]; ok {
			platformSpec = runtimePlatform.Platform
		}
	}
	platform := platforms.Format(platformSpec)

	image, err := c.LocalResolve(imageSpec.GetImage(), runtimeHandler)
	if err != nil {
		if errdefs.IsNotFound(err) {
			span.AddEvent(err.Error())
			// return empty without error when image not found.
			return nil
		}
		return fmt.Errorf("can not resolve %q locally: %w", imageSpec.GetImage(), err)
	}
	span.SetAttributes(tracing.Attribute("image.id", image.Key.ID))
	// Remove all image references.
	for _, ref := range image.References {
		var opts []images.DeleteOpt

		// Remove only the platform label from the containerd image as the image
		// could exist for many platforms with the image pull per runtime class feature.
		ctrdImg, err := c.images.Get(ctx, ref)
		if err != nil {
			continue
		}

		// Delete platform image label from ctrdImg
		updatedImg, err := c.deletePlatformLabelAndUpdateImage(ctx, ctrdImg, platform)
		if err != nil {
			return err
		}

		// Delete ref from CRI image store
		if err := c.imageStore.Update(ctx, ref, platform); err != nil {
			return fmt.Errorf("failed to update image reference %q for %q: %w", ref, image.Key.ID, err)
		}

		if !platformImageLabelExists(updatedImg.Labels) {
			// we removed the last platform label reference from the image, so completely remove
			// this image from containerd store.
			// Delete the last image reference synchronously to trigger garbage collection.
			// This is best effort. It is possible that the image reference is deleted by
			// someone else before this point.
			opts = []images.DeleteOpt{images.SynchronousDelete()}
			err = c.images.Delete(ctx, ref, opts...)
			if err == nil || errdefs.IsNotFound(err) {
				// Update image store to reflect the newest state in containerd.
				if err := c.imageStore.Update(ctx, ref, platform); err != nil {
					return fmt.Errorf("failed to update image reference %q for %q: %w", ref, image.Key.ID, err)
				}
				continue
			}
			return fmt.Errorf("failed to delete image reference %q for %q: %w", ref, image.Key.ID, err)
		}
	}
	return nil
}

// platformImageLabelExists checks if a platform image label
// exists in labels.
func platformImageLabelExists(labels map[string]string) bool {
	platformLabelExists := false
	for key := range labels {
		if strings.HasPrefix(key, ctrdlabels.PlatformLabelPrefix) {
			platformLabelExists = true
			break
		}
	}
	return platformLabelExists
}
