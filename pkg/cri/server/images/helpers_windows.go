//go:build windows

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
	"fmt"

	runhcsoptions "github.com/Microsoft/hcsshim/cmd/containerd-shim-runhcs-v1/options"
	criconfig "github.com/containerd/containerd/v2/pkg/cri/config"
	"github.com/containerd/containerd/v2/platforms"
	"github.com/containerd/containerd/v2/plugins"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func isRuntimeHandlerVMBased(runtimeOpts interface{}) bool {
	rhcso, ok := runtimeOpts.(*runhcsoptions.Options)
	if ok {
		if rhcso == nil {
			return false
		}
		return rhcso.SandboxIsolation == 1 // hyperV Isolated
	}
	return false
}

// For windows: check if the runtime handler has sandbox isolation field set and use
// ociRuntime.Platform for platform matcher only for hyperV isolated runtime handlers.
// Process isolated containers run directly on the host and hence only the default platform
// matcher of the host needs to used. If ociRuntime.Platform was defined for process isolated
// runtime handlers, it would be better to explicitly throw an error here so that user can
// remove ociRuntime.Platform field for this runtime handler from the toml
func GetPlatformMatcherForRuntimeHandler(ociRuntime criconfig.Runtime, runtimeHandler string) (ocispec.Platform, platforms.MatchComparer, error) {
	if ociRuntime.Type != plugins.RuntimeRunhcsV1 {
		return ocispec.Platform{}, nil, fmt.Errorf("not a valid windows runtime")
	}
	// ensure that OSVersion is mentioned for windows runtime handlers
	if ociRuntime.Platform.OSVersion == "" {
		return ocispec.Platform{}, nil, fmt.Errorf("ociruntime.Platform.OSVersion needs to be specified for windows")
	}

	runtimeOpts, err := criconfig.GenerateRuntimeOptions(ociRuntime)
	if err != nil {
		return ocispec.Platform{}, nil, fmt.Errorf("failed to get runtime options for runtime: %v", runtimeHandler)
	}

	if isRuntimeHandlerVMBased(runtimeOpts) {
		// If windows OS, check if hostOSVersion and Platform.OsVersion are compatible.
		// That is, are the host and UVM compatible based on the msdocs compat matricx (mentioned in 4126  KEP).
		isCompat := platforms.AreWindowsHostAndGuestHyperVCompatible(platforms.DefaultSpec(), ociRuntime.Platform)
		if !isCompat {
			return ocispec.Platform{}, nil, fmt.Errorf("incompatible host and guest OSVersions")
		}
		return ociRuntime.Platform, platforms.Only(ociRuntime.Platform), nil
	}
	return ocispec.Platform{}, nil, fmt.Errorf("runtime.Platform cannot override the host platform for process isolation")
}
