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

package netns

import (
	"context"
	"errors"

	"github.com/Microsoft/hcsshim"
	"github.com/Microsoft/hcsshim/hcn"
	"github.com/containerd/containerd/log"
)

var errNotImplementedOnWindows = errors.New("not implemented on windows")

// NetNS holds network namespace for sandbox
type NetNS struct {
	path string
}

// NewNetNS creates a network namespace for the sandbox.
func NewNetNS(baseDir string) (*NetNS, error) {
	temp := hcn.HostComputeNamespace{}

	// Set ReadyOnCreate to true if HNS version >= 15.2 or 13.4 .
	// These versions of HNS change how network compartments are
	// initialized and depend on ReadyOnCreate field for the same.
	// These changes on HNS side were mostly made to support removal
	// of pause containers for windows process isolated scenarios.
	hnsGlobals, err := hcsshim.GetHNSGlobals()
	if err == nil {
		if (hnsGlobals.Version.Major > 15) ||
			(hnsGlobals.Version.Major == 15 && hnsGlobals.Version.Minor >= 2) ||
			(hnsGlobals.Version.Major == 13 && hnsGlobals.Version.Minor >= 4) {
			temp.ReadyOnCreate = true
		}
	}
	log.G(context.Background()).Debugf("NewNetNS(), value of HostComputeNamespace.ReadyOnCreate() is %v", temp.ReadyOnCreate)
	hcnNamespace, err := temp.Create()
	if err != nil {
		return nil, err
	}

	return &NetNS{path: hcnNamespace.Id}, nil
}

// NewNetNS returns the netns from pid or a new netns if pid is 0.
func NewNetNSFromPID(baseDir string, pid uint32) (*NetNS, error) {
	return nil, errNotImplementedOnWindows
}

// LoadNetNS loads existing network namespace.
func LoadNetNS(path string) *NetNS {
	return &NetNS{path: path}
}

// Remove removes network namespace if it exists and not closed. Remove is idempotent,
// meaning it might be invoked multiple times and provides consistent result.
func (n *NetNS) Remove() error {
	hcnNamespace, err := hcn.GetNamespaceByID(n.path)
	if err != nil {
		if hcn.IsNotFoundError(err) {
			return nil
		}
		return err
	}
	err = hcnNamespace.Delete()
	if err == nil || hcn.IsNotFoundError(err) {
		return nil
	}
	return err
}

// Closed checks whether the network namespace has been closed.
func (n *NetNS) Closed() (bool, error) {
	_, err := hcn.GetNamespaceByID(n.path)
	if err == nil {
		return false, nil
	}
	if hcn.IsNotFoundError(err) {
		return true, nil
	}
	return false, err
}

// GetPath returns network namespace path for sandbox container
func (n *NetNS) GetPath() string {
	return n.path
}

// NOTE: Do function is not supported.
