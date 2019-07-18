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

package oc

import (
	"go.opencensus.io/trace"
)

// SetSpanStatus sets `span.SetStatus` to the proper status depending on `err`. If
// `err` is `nil` assumes `trace.StatusCodeOk`.
func SetSpanStatus(span *trace.Span, err error) {
	status := trace.Status{}
	if err != nil {
		// TODO: JTERRY75 - Handle errors in a non-generic way
		status.Code = trace.StatusCodeUnknown
		status.Message = err.Error()
	}
	span.SetStatus(status)
}
