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

	//"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/log"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/usage"
	"github.com/containerd/containerd/pkg/cri/labels"
	"github.com/containerd/containerd/pkg/cri/util"
	"github.com/containerd/containerd/platforms"
	docker "github.com/distribution/reference"

	"github.com/opencontainers/go-digest"
	imagedigest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/go-digest/digestset"
	imageidentity "github.com/opencontainers/image-spec/identity"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
)

// name-runtimehandler
const imageKeyFormat = "%s-%s"

// Image contains all resources associated with the image. All fields
// MUST not be mutated directly after created.
type Image struct {
	// Id of the image. Normally the digest of image config.
	ID string
	// runtime handler used to pull this image.
	RuntimeHandler string
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
	// matchcomparer for each runtime class. For more info, see CriService struct
	platformMatcherMap map[string]platforms.MatchComparer
}

// InfoProvider provides both content and info about content
type InfoProvider interface {
	content.Provider
	Info(ctx context.Context, dgst digest.Digest) (content.Info, error)
}

// Store stores all images.
type Store struct {
	lock sync.RWMutex
	// refCache is a containerd image reference to image id cache.
	refCache map[string]string
	// images is the local image store
	images images.Store

	// content provider
	provider InfoProvider

	// platform represents the currently supported platform for images
	// TODO: Make this store multi-platform
	platform platforms.MatchComparer

	// matchcomparer for each runtime class. For more info, see CriService struct
	platformMatcherMap map[string]platforms.MatchComparer

	// store is the internal image store indexed by image id.
	store *store
}

// NewStore creates an image store.
func NewStore(img images.Store, provider InfoProvider, platform platforms.MatchComparer, platformMatcherMap map[string]platforms.MatchComparer) *Store {
	return &Store{
		refCache: make(map[string]string),
		images:   img,
		provider: provider,
		platform: platform,
		platformMatcherMap: platformMatcherMap,
		store: &store{
			images:    make(map[string]Image),
			digestSet: digestset.NewSet(),
		},
	}
}

// Update updates cache for a reference.
func (s *Store) Update(ctx context.Context, ref string, runtimeHandler string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	log.G(ctx).Debugf("!! pkg.cri.sote Update() inage with ref %v, runtimeHdlr %v", ref, runtimeHandler)
	/*
	getImageOpts := []containerd.GetImageOpt{
		containerd.GetImageWithPlatformMatcher(s.platformMatcherMap[runtimeHandler]),
	}
	*/
	i, err := s.images.Get(ctx, ref)
	log.G(ctx).Debugf("pkg.cri.store Update(), containerd.Image is %v, err: %v", i, err)
	if err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("get image from containerd: %w", err)
	}

	var img *Image
	if err == nil {
		img, err = s.getImage(ctx, i, runtimeHandler)
		log.G(ctx).Debugf("pkg.cri.store Update(), after getImage(), img is %v, err: %v", img, err)
		if err != nil {
			return fmt.Errorf("get image info from containerd: %w", err)
		}
	}
	return s.update(ref, img, runtimeHandler)
}

// update updates the internal cache. img == nil means that
// the image does not exist in containerd.
func (s *Store) update(ref string, img *Image, runtimeHandler string) error {
	// key
	key := fmt.Sprintf(imageKeyFormat, ref, runtimeHandler)
	oldID, oldExist := s.refCache[key] //ref
	log.G(context.Background()).Debugf("!! pkg.cri.sote update() ctrd.image with ref %v, img: %v, runtimeHdlr %v", ref, img, runtimeHandler)
	log.G(context.Background()).Debugf("!! pkg.cri.sote update() oldID %v, oldExist %v", oldID, oldExist)

	if img == nil {
		// The image reference doesn't exist in containerd.
		if oldExist {
			// Remove the reference from the store.
			s.store.delete(oldID, ref, runtimeHandler)
			delete(s.refCache, key) //ref
		}
		return nil
	}
	if oldExist {
		if oldID == img.ID {
			return nil
		}
		// Updated. Remove tag from old image.
		s.store.delete(oldID, ref, img.RuntimeHandler)
	}
	// New image. Add new image.
	s.refCache[key] = img.ID //ref
	return s.store.add(*img)
}

// getImage gets image information from containerd for current platform.
func (s *Store) getImage(ctx context.Context, i images.Image, runtimeHandler string) (*Image, error) {
	log.G(ctx).Debugf("pkg.cri.store getImage(), containerd.Image is %v", i)
	diffIDs, err := i.RootFS(ctx, s.provider, s.platform)
	if err != nil {
		return nil, fmt.Errorf("get image diffIDs: %w", err)
	}
	chainID := imageidentity.ChainID(diffIDs)

	size, err := usage.CalculateImageUsage(ctx, i, s.provider, usage.WithManifestLimit(s.platform, 1), usage.WithManifestUsage())
	if err != nil {
		return nil, fmt.Errorf("get image compressed resource size: %w", err)
	}

	desc, err := i.Config(ctx, s.provider, s.platform)
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
		ID:         id,
		References: []string{i.Name},
		RuntimeHandler: runtimeHandler,
		ChainID:    chainID.String(),
		Size:       size,
		ImageSpec:  spec,
		Pinned:     pinned,
	}, nil

}

// Resolve resolves a image reference to image id.
func (s *Store) Resolve(ref string, runtimeHandler string) (string, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	key := fmt.Sprintf(imageKeyFormat, ref, runtimeHandler)
	id, ok := s.refCache[key] //ref
	if !ok {
		return "", errdefs.ErrNotFound
	}
	return id, nil
}

// Get gets image metadata by image id. The id can be truncated.
// Returns various validation errors if the image id is invalid.
// Returns errdefs.ErrNotFound if the image doesn't exist.
func (s *Store) Get(id, runtimeHandler string) (Image, error) {
	return s.store.get(id, runtimeHandler)
}

// List lists all images.
func (s *Store) List() []Image {
	return s.store.list()
}

type store struct {
	lock      sync.RWMutex
	images    map[string]Image
	digestSet *digestset.Set
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
	if _, err := s.digestSet.Lookup(img.ID); err != nil {
		if err != digestset.ErrDigestNotFound {
			return err
		}
		if err := s.digestSet.Add(imagedigest.Digest(img.ID)); err != nil {
			return err
		}
	}

	key := fmt.Sprintf(imageKeyFormat, img.ID, img.RuntimeHandler)
	log.G(context.Background()).Debugf("!! store.add() , key %v", key)
	i, ok := s.images[key]
	if !ok {
		// If the image doesn't exist, add it.
		s.images[key] = img
		return nil
	}
	// Or else, merge and sort the references.
	i.References = docker.Sort(util.MergeStringSlices(i.References, img.References))
	i.Pinned = i.Pinned || img.Pinned
	s.images[key] = i
	return nil
}

func (s *store) get(id, runtimeHandler string) (Image, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	digest, err := s.digestSet.Lookup(id)
	if err != nil {
		if err == digestset.ErrDigestNotFound {
			err = errdefs.ErrNotFound
		}
		return Image{}, err
	}

	key := fmt.Sprintf(imageKeyFormat, digest.String(), runtimeHandler)
	log.G(context.Background()).Debugf("!! store.get() , key %v", key)
	if i, ok := s.images[key]; ok {
		return i, nil
	}
	return Image{}, errdefs.ErrNotFound
}

func (s *store) delete(id, ref, runtimeHandler string) {
	s.lock.Lock()
	defer s.lock.Unlock()
	log.G(context.Background()).Debugf("!! pkg.cri.sote delete() ctrd.image with ref %v, runtimeHdlr %v", ref, runtimeHandler)
	digest, err := s.digestSet.Lookup(id)
	if err != nil {
		// Note: The idIndex.Delete and delete doesn't handle truncated index.
		// So we need to return if there are error.
		return
	}

	key := fmt.Sprintf(imageKeyFormat, digest.String(), runtimeHandler)
	log.G(context.Background()).Debugf("!! store.delete() , key %v", key)
	i, ok := s.images[key]
	if !ok {
		return
	}
	i.References = util.SubtractStringSlice(i.References, ref)
	if len(i.References) != 0 {
		s.images[key] = i
		return
	}
	// Remove the image if it is not referenced any more.
	s.digestSet.Remove(digest)
	delete(s.images, key)
}
