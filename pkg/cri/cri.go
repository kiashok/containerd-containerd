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
	"flag"
	"fmt"
	"os"
	//"reflect"
	"path/filepath"
	"context"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/pkg/cri/nri"
	"github.com/containerd/containerd/pkg/cri/sbserver"
	nriservice "github.com/containerd/containerd/pkg/nri"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/plugin"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"k8s.io/klog/v2"

	criconfig "github.com/containerd/containerd/pkg/cri/config"
	"github.com/containerd/containerd/pkg/cri/constants"
	"github.com/containerd/containerd/pkg/cri/server"
//	"github.com/pelletier/go-toml"
//	runcoptions "github.com/containerd/containerd/runtime/v2/runc/options"
//	runtimeoptions "github.com/containerd/containerd/pkg/runtimeoptions/v1"
//	runhcsoptions "github.com/Microsoft/hcsshim/cmd/containerd-shim-runhcs-v1/options"
//	runtimeoptions "github.com/containerd/containerd/pkg/runtimeoptions/v1"
)

//const (
		// runtimeRunhcsV1 is the runtime type for runhcs.
//		runtimeRunhcsV1 = "io.containerd.runhcs.v1"
//)

// Register CRI service plugin
func init() {
	config := criconfig.DefaultConfig()
	plugin.Register(&plugin.Registration{
		Type:   plugin.GRPCPlugin,
		ID:     "cri",
		Config: &config,
		Requires: []plugin.Type{
			plugin.EventPlugin,
			plugin.ServicePlugin,
			plugin.NRIApiPlugin,
		},
		InitFn: initCRIService,
	})
}

/*
func generateRuntimeOptions(r criconfig.Runtime, c criconfig.Config) (interface{}, error) {
	if r.Options == nil {
		return nil, nil
	}
	optionsTree, err := toml.TreeFromMap(r.Options)
	if err != nil {
		return nil, err
	}
	options := getRuntimeOptionsType(r.Type)
	if err := optionsTree.Unmarshal(options); err != nil {
		return nil, err
	}

	// For generic configuration, if no config path specified (preserving old behavior), pass
	// the whole TOML configuration section to the runtime.
	if runtimeOpts, ok := options.(*runtimeoptions.Options); ok && runtimeOpts.ConfigPath == "" {
		runtimeOpts.ConfigBody, err = optionsTree.Marshal()
		if err != nil {
			return nil, fmt.Errorf("failed to marshal TOML blob for runtime %q: %v", r.Type, err)
		}
	}

	return options, nil
}
*/
/*
// getRuntimeOptionsType gets empty runtime options by the runtime type name.
func getRuntimeOptionsType(t string) interface{} {
	switch t {
	case plugin.RuntimeRuncV2:
		return &runcoptions.Options{}
	case runtimeRunhcsV1:
		return &runhcsoptions.Options{}
	default:
		return &runtimeoptions.Options{}
	}
}
*/

func initCRIService(ic *plugin.InitContext) (interface{}, error) {
	ready := ic.RegisterReadiness()
	ic.Meta.Platforms = []imagespec.Platform{platforms.DefaultSpec()}
	ic.Meta.Exports = map[string]string{"CRIVersion": constants.CRIVersion}
	ctx := ic.Context
	pluginConfig := ic.Config.(*criconfig.PluginConfig)
	if err := criconfig.ValidatePluginConfig(ctx, pluginConfig); err != nil {
		return nil, fmt.Errorf("invalid plugin config: %w", err)
	}

	c := criconfig.Config{
		PluginConfig:       *pluginConfig,
		ContainerdRootDir:  filepath.Dir(ic.Root),
		ContainerdEndpoint: ic.Address,
		RootDir:            ic.Root,
		StateDir:           ic.State,
	}
	log.G(ctx).Infof("Start cri plugin with config %+v", c)

	if err := setGLogLevel(); err != nil {
		return nil, fmt.Errorf("failed to set glog level: %w", err)
	}

	log.G(ctx).Info("Connect containerd service")
	/*
	imagePlatform := imagespec.Platform {
		Architecture: "",
		OS: "",
		OSFeatures: []string{},
		OSVersion: "",
		Variant: "",
	}
	//imagePlatform := imagespec.Platform{}
	ic.Meta.RuntimeHandler = c.PluginConfig.ContainerdConfig.DefaultRuntimeName
	*/
	client, err := containerd.New(
		"",
		containerd.WithDefaultNamespace(constants.K8sContainerdNamespace),
		containerd.WithDefaultPlatform(platforms.Default()),
//		containerd.WithGuestPlatform(imagePlatform),
	//	containerd.WithRuntimeHandler(c.PluginConfig.ContainerdConfig.DefaultRuntimeName),
	// NOTE: purposely not setting runtime handler here as it is the default client created. We need to use
	// client in the map for image pull purposes
		//containerd.WithRuntimeHandler(k),
		containerd.WithInMemoryServices(ic),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create containerd client: %w", err)
	}

	log.G(ctx).Debugf("!! InitCriService , default original client %v", client)
	platformMap := make(map[string]platforms.MatchComparer)
	for k, r := range c.PluginConfig.ContainerdConfig.Runtimes {
		log.G(context.Background()).Debugf("!! InitCriService k: %v", k)
		log.G(context.Background()).Debugf("!! InitCriService r.GuestPlatform: %v", r.GuestPlatform)

		if r.GuestPlatform.OS != "" && r.GuestPlatform.OSVersion != "" {
			//platformMap[k] = platforms.OnlyWithRuntimeHdlr(copts.guestPlatform, k)
			platformMap[k] = platforms.Only(r.GuestPlatform) // may have to pass runtime handler and set it in platform matcher so the key can be changed in cri's image map
		} else {
			//platformMap[k] = platforms.OnlyWithRuntimeHdlr(platforms.Default(), k)
			platformMap[k] = platforms.Only(platforms.DefaultSpec())
		}
	}
	/*
	clientMap := make(map[string]*containerd.Client)
	for k, r := range c.PluginConfig.ContainerdConfig.Runtimes {
		//guestPlatform := imagespec.Platform{}
		log.G(context.Background()).Debugf("!! InitCriService k: %v", k)
		log.G(context.Background()).Debugf("!! InitCriService r.GuestPlatform: %v", r.GuestPlatform)

		ic.Meta.RuntimeHandler = k

		// test 
		ociRuntime, ok := pluginConfig.ContainerdConfig.Runtimes[k] // read handler.Runtimes.OSVersion etc
		if !ok {
			log.G(context.Background()).Errorf("failed to get runtime %v", k)
			//return nil, fmt.Errorf("no runtime for %q is configured", runtimeHdlr)
		}

		// get runtime options for windows pods and check if its process isolated or hyperV
		if ociRuntime.Type == runtimeRunhcsV1 {
			runtimeOpts, err := generateRuntimeOptions(ociRuntime, c)
			if err != nil {
				//return nil, fmt.Errorf("failed to generate runtime options: %w", err)
				//return c.client
				log.G(context.Background()).Errorf("failed to get runtime options line 150")
			}
			rhcso, ok := runtimeOpts.(*runhcsoptions.Options)
			if ok {
				if rhcso.SandboxIsolation == 1 { // hyperV isolated
					// we need to use the appropriate client using runtimeHdlr for this client.Pull() call
					//image, err := c.clientMap[runtimeHdlr].Pull(pctx, ref, pullOpts...)
					log.G(context.Background()).Debugf("!!*** criservice init, sandbox isolation is 1")
					//return c.clientMap[runtimeHdlr]
					// TODO: above line check for nil
					// why not c.runtime below?
				}
			}
		}
		//

		clientMap[k], err = containerd.New(
			"",
			containerd.WithDefaultNamespace(constants.K8sContainerdNamespace),
			//containerd.WithGuestPlatform(guestPlatform),
			containerd.WithGuestPlatform(r.GuestPlatform),
			//containerd.WithDefaultPlatform(platforms.Default()),
			containerd.WithRuntimeHandler(k),
			containerd.WithInMemoryServices(ic),
		)

		log.G(context.Background()).Debugf("!! initcriservice, in for, client: %v", clientMap[k])
		if err != nil {
			return nil, fmt.Errorf("failed to create containerd clientMap: %w", err)
		}
		//all callers of criservice.client.func/Pull() etc needs to change to
	//	criservice.client[runtime].func() ??
	}
*/
	var s server.CRIService
	if os.Getenv("ENABLE_CRI_SANDBOXES") != "" {
		log.G(ctx).Info("using experimental CRI Sandbox server - unset ENABLE_CRI_SANDBOXES to disable")
		s, err = sbserver.NewCRIService(c, client, getNRIAPI(ic))
	} else {
		log.G(ctx).Debug("using legacy CRI server")
//		s, err = server.NewCRIService(c, client, clientMap, getNRIAPI(ic))
s, err = server.NewCRIService(c, client, platformMap, getNRIAPI(ic))
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create CRI service: %w", err)
	}

	go func() {
		if err := s.Run(ready); err != nil {
			log.G(ctx).WithError(err).Fatal("Failed to run CRI service")
		}
		// TODO(random-liu): Whether and how we can stop containerd.
	}()

	return s, nil
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
		pluginType = plugin.NRIApiPlugin
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
