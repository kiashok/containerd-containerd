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

package image

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/images/usage"
	"github.com/containerd/containerd/v2/internal/cri/labels"
	"github.com/containerd/containerd/v2/internal/cri/util"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	docker "github.com/distribution/reference"
	"k8s.io/apimachinery/pkg/util/sets"

	imagedigest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/go-digest/digestset"
	imageidentity "github.com/opencontainers/image-spec/identity"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

type RefKey struct {
	// Ref of the image
	Ref string
	// Runtimehandler used for pulling this image
	Platform string
}

type ImageIDKey struct {
	// Id of the image. Normally the digest of image config.
	ID string
	// Runtimehandler used for pulling this image
	Platform string
}

// Image contains all resources associated with the image. All fields
// MUST not be mutated directly after created.
type Image struct {
	// Key of the image - (ID, RuntimeHandler)
	Key ImageIDKey
	// References are references to the image, e.g. RepoTag and RepoDigest.
	References []string
	// ChainID is the chainID of the image.
	ChainID string
	// Size is the compressed size of the image.
	Size int64
	// ImageSpec is the oci image structure which describes basic information about the image.
	ImageSpec imagespec.Image
	// Pinned image to prevent it from garbage collection
	Pinned bool
}

// Getter is used to get images but does not make changes
type Getter interface {
	Get(ctx context.Context, name string) (images.Image, error)
}

// Store stores all images.
type Store struct {
	lock sync.RWMutex
	// refCache is a map of containerd image RefKey to ImageID key.
	refCache map[RefKey]ImageIDKey

	// images is the local image store
	images Getter

	// content provider
	provider content.InfoReaderProvider

	// store is the internal image store indexed by image id.
	store *store
}

type store struct {
	lock      sync.RWMutex
	images    map[ImageIDKey]Image
	digestSet *digestset.Set
	// With image pull per runtime class, same image could now be
	// pulled for different platforms. digestReferences keeps track
	// of the list of imageIDKeys that are referencing this image.
	// Images from CRI store are removed only if there are no
	// more images references the image.
	// digestReferences is map if image digest -> imageIDKey referencing the image.
	digestReferences map[string]sets.Set[ImageIDKey]
	// image digest -> RefKey
	pinnedRefs map[string]sets.Set[RefKey]
}

// NewStore creates an image store.
func NewStore(img Getter, provider content.InfoReaderProvider) *Store {
	return &Store{
		refCache: make(map[RefKey]ImageIDKey),
		images:   img,
		provider: provider,
		store: &store{
			images:           make(map[ImageIDKey]Image),
			digestSet:        digestset.NewSet(),
			digestReferences: make(map[string]sets.Set[ImageIDKey]),
			pinnedRefs:       make(map[string]sets.Set[RefKey]),
		},
	}
}

// Update updates cache for a reference.
func (s *Store) Update(ctx context.Context, ref string, platform string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	// Get containerd image from containerd image store
	i, err := s.images.Get(ctx, ref)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("get image from containerd: %w", err)
		} else {
			// if image was not found in the local image store,
			// make sure to remove all the references of 'ref' from CRI's image store cache.
			return s.removeAllReferences(ctx, ref)
		}
	}

	var img *Image
	if err == nil {
		// Get image from CRI image store
		img, err = s.getImage(ctx, i, platform)
		if err != nil {
			return fmt.Errorf("get image info from containerd: %w", err)
		}
	}
	refKey := RefKey{Ref: ref, Platform: platform}
	return s.update(refKey, img)
}

// Remove all references from CRI image store cache, if the image was not
// found in containerd image store.
func (s *Store) removeAllReferences(ctx context.Context, ref string) error {
	for refKey, imageIDKey := range s.refCache {
		if refKey.Ref == ref {
			// Remove the reference from the store.
			s.store.delete(imageIDKey.ID, refKey)
			delete(s.refCache, refKey)
		}
	}
	return nil
}

// update updates the internal cache. img == nil means that
// the image does not exist in containerd.
func (s *Store) update(refKey RefKey, img *Image) error {
	oldImageIDKey, oldExist := s.refCache[refKey]
	if img == nil {
		// The image reference doesn't exist in containerd.
		if oldExist {
			// Remove the reference from the store.
			s.store.delete(oldImageIDKey.ID, refKey)
			delete(s.refCache, refKey)
		}
		return nil
	}
	if oldExist {
		if oldImageIDKey.ID == img.Key.ID {
			if s.store.isPinned(img.Key.ID, refKey) == img.Pinned {
				return nil
			}
			if img.Pinned {
				return s.store.pin(img.Key.ID, refKey)
			}
			return s.store.unpin(img.Key.ID, refKey)
		}
		// Updated. Remove tag from old image.
		s.store.delete(oldImageIDKey.ID, refKey)
	}
	// New image. Add new image.
	s.refCache[refKey] = img.Key
	return s.store.add(*img)
}

// getImage gets image information from containerd for current platform.
func (s *Store) getImage(ctx context.Context, i images.Image, platform string) (*Image, error) {
	var platformMatcher platforms.MatchComparer
	if platform == "" {
		platformMatcher = platforms.Default()
	} else {
		platformMatcher = platforms.Only(platforms.MustParse(platform))
	}

	diffIDs, err := i.RootFS(ctx, s.provider, platformMatcher)
	if err != nil {
		return nil, fmt.Errorf("get image diffIDs: %w", err)
	}
	chainID := imageidentity.ChainID(diffIDs)

	size, err := usage.CalculateImageUsage(ctx, i, s.provider, usage.WithManifestLimit(platformMatcher, 1), usage.WithManifestUsage())
	if err != nil {
		return nil, fmt.Errorf("get image compressed resource size: %w", err)
	}

	desc, err := i.Config(ctx, s.provider, platformMatcher)
	if err != nil {
		return nil, fmt.Errorf("get image config descriptor: %w", err)
	}
	id := desc.Digest.String()

	blob, err := content.ReadBlob(ctx, s.provider, desc)
	if err != nil {
		return nil, fmt.Errorf("read image config from content store: %w", err)
	}

	var spec imagespec.Image
	if err := json.Unmarshal(blob, &spec); err != nil {
		return nil, fmt.Errorf("unmarshal image config %s: %w", blob, err)
	}

	pinned := i.Labels[labels.PinnedImageLabelKey] == labels.PinnedImageLabelValue

	return &Image{
		Key:        ImageIDKey{ID: id, Platform: platform},
		References: []string{i.Name},
		ChainID:    chainID.String(),
		Size:       size,
		ImageSpec:  spec,
		Pinned:     pinned,
	}, nil

}

// Resolve resolves a image reference to image id.
func (s *Store) Resolve(ref string, platform string) (string, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	refKey := RefKey{Ref: ref, Platform: platform}
	imageIDKey, ok := s.refCache[refKey]
	if !ok {
		return "", errdefs.ErrNotFound
	}
	return imageIDKey.ID, nil
}

// Get gets image metadata by image id. The id can be truncated.
// Returns various validation errors if the image id is invalid.
// Returns errdefs.ErrNotFound if the image doesn't exist.
func (s *Store) Get(imageID string, platform string) (Image, error) {
	return s.store.get(imageID, platform)
}

// List lists all images.
func (s *Store) List() []Image {
	return s.store.list()
}

func (s *store) list() []Image {
	s.lock.RLock()
	defer s.lock.RUnlock()
	var images []Image
	for _, i := range s.images {
		images = append(images, i)
	}
	return images
}

func (s *store) add(img Image) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	if _, err := s.digestSet.Lookup(img.Key.ID); err != nil {
		if err != digestset.ErrDigestNotFound {
			return err
		}
		if err := s.digestSet.Add(imagedigest.Digest(img.Key.ID)); err != nil {
			return err
		}
	}

	if len(s.digestReferences[img.Key.ID]) == 0 {
		s.digestReferences[img.Key.ID] = sets.New(img.Key)
	} else {
		s.digestReferences[img.Key.ID].Insert(img.Key)
	}

	if img.Pinned {
		var refCacheKeys []RefKey
		for _, references := range img.References {
			refCacheKeys = append(refCacheKeys, RefKey{Ref: references, Platform: img.Key.Platform})
		}

		if refs := s.pinnedRefs[img.Key.ID]; refs == nil {
			s.pinnedRefs[img.Key.ID] = sets.New(refCacheKeys...)
		} else {
			refs.Insert(refCacheKeys...)
		}
	}

	i, ok := s.images[img.Key]
	if !ok {
		// If the image doesn't exist, add it.
		s.images[img.Key] = img
		return nil
	}
	// Or else, merge and sort the references.
	i.References = docker.Sort(util.MergeStringSlices(i.References, img.References))
	i.Pinned = i.Pinned || img.Pinned
	s.images[img.Key] = i
	return nil
}

func (s *store) isPinned(imageID string, refKey RefKey) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	digest, err := s.digestSet.Lookup(imageID)
	if err != nil {
		return false
	}
	refs := s.pinnedRefs[digest.String()]
	return refs != nil && refs.Has(refKey)
}

func (s *store) pin(imageID string, refKey RefKey) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	digest, err := s.digestSet.Lookup(imageID)
	if err != nil {
		if err == digestset.ErrDigestNotFound {
			err = errdefs.ErrNotFound
		}
		return err
	}
	imageIDKey := ImageIDKey{ID: digest.String(), Platform: refKey.Platform}
	i, ok := s.images[imageIDKey]
	if !ok {
		return errdefs.ErrNotFound
	}

	if refs := s.pinnedRefs[digest.String()]; refs == nil {
		s.pinnedRefs[digest.String()] = sets.New(refKey)
	} else {
		refs.Insert(refKey)
	}
	i.Pinned = true
	s.images[imageIDKey] = i
	return nil
}

func (s *store) unpin(imageID string, refKey RefKey) error {
	s.lock.Lock()
	defer s.lock.Unlock()
	digest, err := s.digestSet.Lookup(imageID)
	if err != nil {
		if err == digestset.ErrDigestNotFound {
			err = errdefs.ErrNotFound
		}
		return err
	}
	imageIDKey := ImageIDKey{ID: digest.String(), Platform: refKey.Platform}
	i, ok := s.images[imageIDKey]
	if !ok {
		return errdefs.ErrNotFound
	}

	refs := s.pinnedRefs[digest.String()]
	if refs == nil {
		return nil
	}
	refs.Delete(refKey)

	// delete unpinned image, we only need to keep the pinned
	// entries in the map. Since an image digest can now be
	// referenced by multiple platforms, we need to check that
	// there are no more entries left before deleting.
	if len(refs) == 0 {
		delete(s.pinnedRefs, digest.String())
	}
	i.Pinned = false
	s.images[imageIDKey] = i
	return nil
}

func (s *store) get(imageID string, platform string) (Image, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	digest, err := s.digestSet.Lookup(imageID)
	if err != nil {
		if err == digestset.ErrDigestNotFound {
			err = errdefs.ErrNotFound
		}
		return Image{}, err
	}
	imageIDKey := ImageIDKey{ID: digest.String(), Platform: platform}
	if i, ok := s.images[imageIDKey]; ok {
		return i, nil
	}
	return Image{}, errdefs.ErrNotFound
}

func (s *store) delete(imageID string, refKey RefKey) {
	s.lock.Lock()
	defer s.lock.Unlock()
	digest, err := s.digestSet.Lookup(imageID)
	if err != nil {
		// Note: The idIndex.Delete and delete doesn't handle truncated index.
		// So we need to return if there are error.
		return
	}
	imageIDKey := ImageIDKey{ID: digest.String(), Platform: refKey.Platform}
	i, ok := s.images[imageIDKey]
	if !ok {
		return
	}
	i.References = util.SubtractStringSlice(i.References, refKey.Ref)
	if len(i.References) != 0 {
		if refs := s.pinnedRefs[digest.String()]; refs != nil {
			if refs.Delete(refKey); len(refs) == 0 {
				// we are deleting the last ref for this platform, so
				// set i.Pinned to false
				i.Pinned = false
				// delete unpinned image, we only need to keep the pinned
				// entries in the map
				delete(s.pinnedRefs, digest.String())
			}
		}
		s.images[imageIDKey] = i
		return
	}

	// if i.References == 0, this is the last reference of this imageIdKey.
	// Therefore, remove it from the list of digestReferences as well
	delete(s.digestReferences[digest.String()], imageIDKey)
	delete(s.images, imageIDKey)

	// Remove the image if it is not referenced any more.
	if len(s.digestReferences[digest.String()]) == 0 {
		s.digestSet.Remove(digest)
	}

	if len(s.pinnedRefs[digest.String()]) == 0 {
		delete(s.pinnedRefs, digest.String())
	}
}
