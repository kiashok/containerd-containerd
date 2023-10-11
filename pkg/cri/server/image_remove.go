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

package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/labels"
	"github.com/containerd/containerd/tracing"
	"github.com/pkg/errors"

	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// RemoveImage removes the image.
// TODO(random-liu): Update CRI to pass image reference instead of ImageSpec. (See
// kubernetes/kubernetes#46255)
// TODO(random-liu): We should change CRI to distinguish image id and image spec.
// Remove the whole image no matter the it's image id or reference. This is the
// semantic defined in CRI now.
func (c *criService) RemoveImage(ctx context.Context, r *runtime.RemoveImageRequest) (*runtime.RemoveImageResponse, error) {
	span := tracing.SpanFromContext(ctx)

	// get runtime handler from pull request or use defaut runtime class name if one
	// was not specified
	runtimeHdlr := r.GetImage().GetRuntimeHandler()
	if runtimeHdlr == "" {
		runtimeHdlr = c.config.ContainerdConfig.DefaultRuntimeName
	}
	image, err := c.localResolve(r.GetImage().GetImage(), runtimeHdlr)
	if err != nil {
		if errdefs.IsNotFound(err) {
			span.AddEvent(err.Error())
			// return empty without error when image not found.
			return &runtime.RemoveImageResponse{}, nil
		}
		return nil, fmt.Errorf("can not resolve %q locally: %w", r.GetImage().GetImage(), err)
	}
	span.SetAttributes(tracing.Attribute("image.id", image.ID))

	// Remove all image references.
	for i, ref := range image.References {
		var opts []images.DeleteOpt
		deleteImage := false
		if i == len(image.References)-1 {
			// Delete the last image reference synchronously to trigger garbage collection.
			// This is best effort. It is possible that the image reference is deleted by
			// someone else before this point.
			opts = []images.DeleteOpt{images.SynchronousDelete()}
		}

		// check image runtimehandler labels for the runtimeHdlr
		// here check if is.Get(), check label for runtime handler exists and if so only update label without calling delete if len(numLabels) >1)
		// else call delete
		//newRuntimeHandlerValue := ""
		existingImg, err := c.client.ImageService().Get(ctx, ref)
		if err == nil {
			// check for runtime label in exisitingImg
			if existingImg.Labels[labels.RuntimeHandlerLabel] != "" {
				runtimeHandlerValues := strings.Split(existingImg.Labels[labels.RuntimeHandlerLabel], ",")
				if len(runtimeHandlerValues) > 1 {
					// remove just this runtimeHandler
					newRuntimeHandlerValue, err := labels.CheckAndRemoveRuntimeHandlerLabel(runtimeHandlerValues, runtimeHdlr)
					if err != nil {
						// TODO: return error
						return nil, errors.Wrapf(err, "error trying to remove label %v", runtimeHdlr)
					}
					// update the imageservice
					_, err = c.client.ImageService().Update(ctx, existingImg, "labels."+newRuntimeHandlerValue)
					if err != nil {
						// if image was removed, try create again
						if !errdefs.IsNotFound(err) {
							return nil, errors.Wrapf(err, "failed to update containerd image %v, with new labels %v", existingImg, newRuntimeHandlerValue)
						}
					}
				} else {
					deleteImage = true
				}
			}
		}

		if deleteImage == true {
			err = c.client.ImageService().Delete(ctx, ref, opts...)
			if err == nil || errdefs.IsNotFound(err) {
				// Update image store to reflect the newest state in containerd.
				if err := c.imageStore.Update(ctx, ref, runtimeHdlr); err != nil { //TODO: Test!
					return nil, fmt.Errorf("failed to update image reference %q for %q: %w", ref, image.ID, err)
				}
				continue
			}
			return nil, fmt.Errorf("failed to delete image reference %q for %q: %w", ref, image.ID, err)
		} else {
			// just update the image store and continue
			// Update image store to reflect the newest state in containerd.
			// TODO: should this be delete to be more straight-forward?
			if err := c.imageStore.Update(ctx, ref, runtimeHdlr); err != nil { //TODO: Test!
				return nil, fmt.Errorf("failed to update image reference %q for %q: %w", ref, image.ID, err)
			}
			continue
		}
	}
	return &runtime.RemoveImageResponse{}, nil
}
