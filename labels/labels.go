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

package labels

import (
	"context"
	"strings"

	"github.com/containerd/containerd/log"
)

// LabelUncompressed is added to compressed layer contents.
// The value is digest of the uncompressed content.
const LabelUncompressed = "containerd.io/uncompressed"

// LabelSharedNamespace is added to a namespace to allow that namespaces
// contents to be shared.
const LabelSharedNamespace = "containerd.io/namespace.shareable"

// LabelDistributionSource is added to content to indicate its origin.
// e.g., "containerd.io/distribution.source.docker.io=library/redis"
const LabelDistributionSource = "containerd.io/distribution.source"

const RuntimeHandlerLabel = "containerd.io/runtimehandler"

func CheckAndAppendRuntimeHandlerLabel(currentLabel string, runtimeHandler string) (string, error) {
	// check if the label is valid
	newRuntimeHandlerLabel := currentLabel
	if !strings.Contains(currentLabel, runtimeHandler) {
		newRuntimeHandlerLabel = currentLabel + "," + runtimeHandler
	}

	// The label might hit the limitation of label size, so we need to validate the length
	if err := Validate(RuntimeHandlerLabel, newRuntimeHandlerLabel); err != nil {
		log.G(context.Background()).Warnf("skip to append distribution label: %s", err)
		return "", err
	}

	return newRuntimeHandlerLabel, nil
}

func CheckAndRemoveRuntimeHandlerLabel(runtimeHandlerValues []string, runtimeHandler string) (string, error) {
	newLabel := ""
	for _, val := range runtimeHandlerValues {
		if val == runtimeHandler {
			continue
		} else {
			if newLabel != "" {
				newLabel = newLabel + "," + val
			} else {
				newLabel = val
			}
		}
	}

	// The label might hit the limitation of label size, so we need to validate the length
	if err := Validate(RuntimeHandlerLabel, newLabel); err != nil {
		log.G(context.Background()).Warnf("skip to append distribution label: %s", err)
		return "", err
	}
	return newLabel, nil
}
