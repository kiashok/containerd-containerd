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
	"sync"

	"github.com/containerd/containerd/v2/core/images"
	ctrdlabels "github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/log"
	"github.com/containerd/platforms"
)

func getPlatformsFromImageLabel(imgLabels map[string]string) []string {
	var platformsForImage []string
	for key, value := range imgLabels {
		if strings.HasPrefix(key, ctrdlabels.PlatformLabelPrefix) {
			platformsForImage = append(platformsForImage, value)
		}
	}
	return platformsForImage
}

func (c *CRIImageService) getSupportedSnapshotsForPlatform(runtimePlatforms map[string]ImagePlatform, platform string) []string {
	// always have the default snapshotter for unpacking
	supportedSnapshotter := []string{c.config.Snapshotter}
	for _, imagePlatform := range runtimePlatforms {
		if platforms.Format(imagePlatform.Platform) == platform {
			supportedSnapshotter = append(supportedSnapshotter, imagePlatform.Snapshotter)
		}
	}
	return supportedSnapshotter
}

// LoadImages checks all existing images to ensure they are ready to
// be used for CRI. It may try to recover images which are not ready
// but will only log errors, not return any.
func (c *CRIImageService) CheckImages(ctx context.Context) error {
	// TODO: Move way from `client.ListImages` to directly using image store
	cImages, err := c.client.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("unable to list images: %w", err)
	}

	//defaultSnapshotter := c.config.Snapshotter
	var wg sync.WaitGroup
	for _, i := range cImages {
		wg.Add(1)
		i := i
		// list of platform labels and snapshotters for that platform
		imagePlatforms := getPlatformsFromImageLabel(i.Labels())
		go func(imagePlatforms []string) {
			defer wg.Done()
			for _, imgPlatform := range imagePlatforms {
				// Support all snapshotters
				snapshotters := c.getSupportedSnapshotsForPlatform(c.runtimePlatforms, imgPlatform)
				for _, snapshotter := range snapshotters {

					// TODO: Check platform/snapshot combination. Snapshot check should come first
					ok, _, _, _, err := images.Check(ctx, i.ContentStore(), i.Target(), platforms.Only(platforms.MustParse(imgPlatform)))
					if err != nil {
						log.G(ctx).WithError(err).Errorf("Failed to check image content readiness for %q", i.Name())
						return
					}
					if !ok {
						log.G(ctx).Warnf("The image content readiness for %q is not ok", i.Name())
						return
					}
					// Checking existence of top-level snapshot for each image being recovered.
					// TODO: This logic should be done elsewhere and owned by the image service
					unpacked, err := i.IsUnpacked(ctx, snapshotter)
					if err != nil {
						log.G(ctx).WithError(err).Warnf("Failed to check whether image is unpacked for image %s", i.Name())
						return
					}
					if !unpacked {
						log.G(ctx).Warnf("The image %s is not unpacked.", i.Name())
						// TODO(random-liu): Consider whether we should try unpack here.
					}
					if err := c.UpdateImage(ctx, i.Name()); err != nil {
						log.G(ctx).WithError(err).Warnf("Failed to update reference for image %q", i.Name())
						return
					}
					log.G(ctx).Debugf("Loaded image %q", i.Name())
				}
			}
		}(imagePlatforms)
	}
	wg.Wait()
	return nil
}
