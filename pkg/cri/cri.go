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

package cri

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"k8s.io/klog/v2"

	containerd "github.com/containerd/containerd/v2/client"
	criconfig "github.com/containerd/containerd/v2/pkg/cri/config"
	"github.com/containerd/containerd/v2/pkg/cri/constants"
	"github.com/containerd/containerd/v2/pkg/cri/nri"
	"github.com/containerd/containerd/v2/pkg/cri/server"
	nriservice "github.com/containerd/containerd/v2/pkg/nri"
	"github.com/containerd/containerd/v2/platforms"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/containerd/v2/services/warning"
)

// Register CRI service plugin
func init() {
	config := criconfig.DefaultConfig()
	registry.Register(&plugin.Registration{
		Type:   plugins.GRPCPlugin,
		ID:     "cri",
		Config: &config,
		Requires: []plugin.Type{
			plugins.EventPlugin,
			plugins.ServicePlugin,
			plugins.NRIApiPlugin,
			plugins.WarningPlugin,
			plugins.SandboxControllerPlugin,
		},
		InitFn: initCRIService,
	})
}

func initCRIService(ic *plugin.InitContext) (interface{}, error) {
	ic.Meta.Platforms = []imagespec.Platform{platforms.DefaultSpec()}
	ic.Meta.Exports = map[string]string{"CRIVersion": constants.CRIVersion}
	ctx := ic.Context
	pluginConfig := ic.Config.(*criconfig.PluginConfig)
	if warnings, err := criconfig.ValidatePluginConfig(ctx, pluginConfig); err != nil {
		return nil, fmt.Errorf("invalid plugin config: %w", err)
	} else if len(warnings) > 0 {
		ws, err := ic.GetSingle(plugins.WarningPlugin)
		if err != nil {
			return nil, err
		}
		warn := ws.(warning.Service)
		for _, w := range warnings {
			warn.Emit(ctx, w)
		}
	}

	c := criconfig.Config{
		PluginConfig:       *pluginConfig,
		ContainerdRootDir:  filepath.Dir(ic.Properties[plugins.PropertyRootDir]),
		ContainerdEndpoint: ic.Properties[plugins.PropertyGRPCAddress],
		RootDir:            ic.Properties[plugins.PropertyRootDir],
		StateDir:           ic.Properties[plugins.PropertyStateDir],
	}
	log.G(ctx).Infof("Start cri plugin with config %+v", c)

	if err := setGLogLevel(); err != nil {
		return nil, fmt.Errorf("failed to set glog level: %w", err)
	}

	// initialize matchComparer for each runtime handler defined in containerd toml
	platformMap := make(map[string]platforms.MatchComparer)
	err := initializePlatformMatcherMap(ctx, c, platformMap)
	if err != nil {
		return nil, err
	}

	log.G(ctx).Info("Connect containerd service")
	client, err := containerd.New(
		"",
		containerd.WithDefaultNamespace(constants.K8sContainerdNamespace),
		containerd.WithDefaultPlatform(platforms.Default()),
		containerd.WithInMemoryServices(ic),
		containerd.WithInMemorySandboxControllers(ic),
		containerd.WithPlatformMatcherMap(platformMap),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create containerd client: %w", err)
	}

	s, err := server.NewCRIService(c, client, platformMap, getNRIAPI(ic))
	if err != nil {
		return nil, fmt.Errorf("failed to create CRI service: %w", err)
	}

	// RegisterReadiness() must be called after NewCRIService(): https://github.com/containerd/containerd/issues/9163
	ready := ic.RegisterReadiness()
	go func() {
		if err := s.Run(ready); err != nil {
			log.G(ctx).WithError(err).Fatal("Failed to run CRI service")
		}
		// TODO(random-liu): Whether and how we can stop containerd.
	}()

	return s, nil
}

func initializePlatformMatcherMap(ctx context.Context, c criconfig.Config, platformMap map[string]platforms.MatchComparer) error {
	for k, ociRuntime := range c.PluginConfig.ContainerdConfig.Runtimes {
		// consider guestPlatform values only if OS and Architecture are specified
		if ociRuntime.GuestPlatform.OS != "" && ociRuntime.GuestPlatform.Architecture != "" {
			// For windows: check if the runtime handler has sandbox isolation field set and use
			// guestplatform for platform matcher only for hyperV isolated runtime handlers.
			// Process isolated containers run direclty on the host and hence only the default platform
			// matcher of the host needs to used. If guestPlatform was defined for process isolated
			// runtime handlers, it would be better to explicitly throw an error here so that user can
			// remove guestPlatform field for this runtime handler from the toml
			if ociRuntime.Type == server.RuntimeRunhcsV1 {
				platformMatchComparer, err := getWindowsGuestPlatformMatcher(ctx, ociRuntime, k)
				if err != nil {
					return fmt.Errorf("failed to init platformMap: %w", err)
				}
				platformMap[k] = platformMatchComparer
			} else {
				platformMap[k] = platforms.Only(ociRuntime.GuestPlatform)
			}
		} else {
			platformMap[k] = platforms.Only(platforms.DefaultSpec())
		}
	}

	return nil
}

func getWindowsGuestPlatformMatcher(ctx context.Context, ociRuntime criconfig.Runtime, runtimeHandler string) (platforms.MatchComparer, error) {
	runtimeOpts, err := server.GenerateRuntimeOptions(ociRuntime)
	if err != nil {
		return nil, fmt.Errorf("failed to get runtime options for runtime: %v", runtimeHandler)
	}
	if server.IsWindowsSandboxIsolation(ctx, runtimeOpts) {
		// ensure that OSVersion is mentioned for windows runtime handlers
		if ociRuntime.GuestPlatform.OSVersion == "" {
			return nil, fmt.Errorf("guestPlatform.OSVersion needs to be specified")
		}

		// ***TODO***: If windows OS, check if hostOSVersion and guestPlatform.OsVersion are compatible.
		// that is, are the host and UVM compatible based on the msdocs compat matricx (mentioned in 4126  KEP).
		isCompat := platforms.AreHostAndGuestHyperVCompatible(platforms.DefaultSpec(), ociRuntime.GuestPlatform)
		if !isCompat {
			return nil, fmt.Errorf("incompatible host and guest OSVersions")
		}
		return platforms.Only(ociRuntime.GuestPlatform), nil
	} else {
		return nil, fmt.Errorf("GuestPlatform cannot override the host platform for process isolation")
	}
}

// Set glog level.
func setGLogLevel() error {
	l := log.GetLevel()
	fs := flag.NewFlagSet("klog", flag.PanicOnError)
	klog.InitFlags(fs)
	if err := fs.Set("logtostderr", "true"); err != nil {
		return err
	}
	switch l {
	case log.TraceLevel:
		return fs.Set("v", "5")
	case log.DebugLevel:
		return fs.Set("v", "4")
	case log.InfoLevel:
		return fs.Set("v", "2")
	default:
		// glog doesn't support other filters. Defaults to v=0.
	}
	return nil
}

// Get the NRI plugin, and set up our NRI API for it.
func getNRIAPI(ic *plugin.InitContext) *nri.API {
	const (
		pluginType = plugins.NRIApiPlugin
		pluginName = "nri"
	)

	ctx := ic.Context

	p, err := ic.GetByID(pluginType, pluginName)
	if err != nil {
		log.G(ctx).Info("NRI service not found, NRI support disabled")
		return nil
	}

	api, ok := p.(nriservice.API)
	if !ok {
		log.G(ctx).Infof("NRI plugin (%s, %q) has incorrect type %T, NRI support disabled",
			pluginType, pluginName, api)
		return nil
	}

	log.G(ctx).Info("using experimental NRI integration - disable nri plugin to prevent this")

	return nri.NewAPI(api)
}
