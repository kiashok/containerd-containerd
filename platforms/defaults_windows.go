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

package platforms

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/containerd/containerd/log"
	"github.com/Microsoft/hcsshim/osversion"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/windows"
)

// DefaultSpec returns the current platform's default platform specification.
func DefaultSpec() specs.Platform {
	major, minor, build := windows.RtlGetNtVersionNumbers()
	return specs.Platform{
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		OSVersion:    fmt.Sprintf("%d.%d.%d", major, minor, build),
		// The Variant field will be empty if arch != ARM.
		Variant: cpuVariant(),
	}
}

type windowsmatcher struct {
	specs.Platform
	osVersionPrefix string
	defaultMatcher  Matcher
}

// Match matches platform with the same windows major, minor
// and build version.
func (m windowsmatcher) Match(p specs.Platform) bool {
	match := m.defaultMatcher.Match(p)

	if match && m.OS == "windows" {
		hostMajorMinorVersion := majorMinor(m.osVersionPrefix)
		hostOsBuildNum := buildNumber(m.osVersionPrefix)
		ctrMajorMinorVersion := majorMinor(p.OSVersion)
		ctrOsBuildNum := buildNumber(p.OSVersion)
		
		if hostMajorMinorVersion != ctrMajorMinorVersion {
			return false
		}

		// TODO: Add the matrix for supporting previous ltsc version if host is annual release
		// if host is ltsc then supports only itself and not older ones?
		if hostOsBuildNum >= osversion.V21H2Server || hostOsBuildNum == osversion.V21H2Win11 {
			log.G(context.Background()).Debugf("!! in windowsmatcher match(), >= WS2022/Win11")
			return (ctrOsBuildNum >= osversion.V21H2Server)
		}

		// orig
		if strings.HasPrefix(p.OSVersion, m.osVersionPrefix) {
			return true
		}
		return p.OSVersion == ""
	}

	return match
}

// Less sorts matched platforms in front of other platforms.
// For matched platforms, it puts platforms with larger revision
// number in front.
func (m windowsmatcher) Less(p1, p2 specs.Platform) bool {
	m1, m2 := m.Match(p1), m.Match(p2)
	if m1 && m2 {
		r1, r2 := revision(p1.OSVersion), revision(p2.OSVersion)
		return r1 > r2
	}
	return m1 && !m2
}

func majorMinor(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[0:2], ".")
}

func buildNumber(v string) int {
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return 0
	}	
	
	r, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0
	}
	return r
}

func revision(v string) int {
	parts := strings.Split(v, ".")
	if len(parts) < 4 {
		return 0
	}
	r, err := strconv.Atoi(parts[3])
	if err != nil {
		return 0
	}
	return r
}

func prefix(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 4 {
		return v
	}
	return strings.Join(parts[0:3], ".")
}

// Default returns the current platform's default platform specification.
func Default() MatchComparer {
	return Only(DefaultSpec())
}
