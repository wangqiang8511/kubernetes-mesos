/*
Copyright 2014 Google Inc. All rights reserved.

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

// The controller manager is responsible for monitoring replication
// controllers, and creating corresponding pods to achieve the desired
// state.  It uses the API to listen for new controllers and to create/delete
// pods.
package controllermanager

// HACK(jdef): copy/pasted from k8s /cmd/controller-manager package, hacked to use
// a modified endpoint-controller

import (
	"net"
	"net/http"
	"strconv"

	"github.com/GoogleCloudPlatform/kubernetes/cmd/kube-controller-manager/app"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/resource"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/clientcmd"
	clientcmdapi "github.com/GoogleCloudPlatform/kubernetes/pkg/client/clientcmd/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/cloudprovider"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/cloudprovider/mesos"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/cloudprovider/nodecontroller"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/cloudprovider/servicecontroller"
	replicationControllerPkg "github.com/GoogleCloudPlatform/kubernetes/pkg/controller"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/healthz"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/namespace"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/resourcequota"
	kendpoint "github.com/GoogleCloudPlatform/kubernetes/pkg/service"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/serviceaccount"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/volumeclaimbinder"
	"github.com/GoogleCloudPlatform/kubernetes/plugin/contrib/mesos/pkg/profile"

	"github.com/golang/glog"
	kmendpoint "github.com/mesosphere/kubernetes-mesos/pkg/service"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/pflag"
)

// CMServer is the mail context object for the controller manager.
type CMServer struct {
	*app.CMServer
	UseHostPortEndpoints bool
}

// NewCMServer creates a new CMServer with a default config.
func NewCMServer() *CMServer {
	s := &CMServer{
		CMServer: app.NewCMServer(),
	}
	s.CloudProvider = mesos.PluginName
	s.UseHostPortEndpoints = true
	s.MinionRegexp = "^.*$"
	// if the kubelet isn't running on a slave we don't want the resources to show up on the node
	s.NodeMilliCPU = 0
	s.NodeMemory = *resource.NewQuantity(0, resource.BinarySI)
	return s
}

// AddFlags adds flags for a specific CMServer to the specified FlagSet
func (s *CMServer) AddFlags(fs *pflag.FlagSet) {
	s.CMServer.AddFlags(fs)
	fs.BoolVar(&s.UseHostPortEndpoints, "host_port_endpoints", s.UseHostPortEndpoints, "Map service endpoints to hostIP:hostPort instead of podIP:containerPort. Default true.")
}

func (s *CMServer) verifyMinionFlags() {
	if !s.SyncNodeList && s.MinionRegexp != "" {
		glog.Info("--minion_regexp is ignored by --sync_nodes=false")
	}
	if s.CloudProvider == "" || s.MinionRegexp == "" {
		if len(s.MachineList) == 0 {
			glog.Info("No machines specified!")
		}
		return
	}
	if len(s.MachineList) != 0 {
		glog.Info("--machines is overwritten by --minion_regexp")
	}
}

func (s *CMServer) Run(_ []string) error {
	s.verifyMinionFlags()

	if s.Kubeconfig == "" && s.Master == "" {
		glog.Warningf("Neither --kubeconfig nor --master was specified.  Using default API client.  This might not work.")
	}

	// This creates a client, first loading any specified kubeconfig
	// file, and then overriding the Master flag, if non-empty.
	kubeconfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: s.Kubeconfig},
		&clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: s.Master}}).ClientConfig()
	if err != nil {
		return err
	}

	kubeconfig.QPS = 20.0
	kubeconfig.Burst = 30

	kubeClient, err := client.New(kubeconfig)
	if err != nil {
		glog.Fatalf("Invalid API configuration: %v", err)
	}

	go func() {
		mux := http.NewServeMux()
		healthz.InstallHandler(mux)
		if s.EnableProfiling {
			profile.InstallHandler(mux)
		}
		mux.Handle("/metrics", prometheus.Handler())
		server := &http.Server{
			Addr:    net.JoinHostPort(s.Address.String(), strconv.Itoa(s.Port)),
			Handler: mux,
		}
		glog.Fatal(server.ListenAndServe())
	}()

	endpoints := s.createEndpointController(kubeClient)
	go endpoints.Run(s.ConcurrentEndpointSyncs, util.NeverStop)

	controllerManager := replicationControllerPkg.NewReplicationManager(kubeClient, replicationControllerPkg.BurstReplicas)
	go controllerManager.Run(s.ConcurrentRCSyncs, util.NeverStop)

	//TODO(jdef) should eventually support more cloud providers here
	if s.CloudProvider != mesos.PluginName {
		glog.Fatalf("Unsupported cloud provider: %v", s.CloudProvider)
	}
	cloud := cloudprovider.InitCloudProvider(s.CloudProvider, s.CloudConfigFile)

	//this is the default "static" capacity reported when there's no kubelet running on a slave
	nodeResources := &api.NodeResources{
		Capacity: api.ResourceList{
			api.ResourceCPU:    *resource.NewMilliQuantity(s.NodeMilliCPU, resource.DecimalSI),
			api.ResourceMemory: s.NodeMemory,
		},
	}

	if s.SyncNodeStatus {
		glog.Warning("DEPRECATION NOTICE: sync-node-status flag is being deprecated. It has no effect now and it will be removed in a future version.")
	}

	nodeController := nodecontroller.NewNodeController(cloud, s.MinionRegexp, nil, nodeResources,
		kubeClient, s.RegisterRetryCount, s.PodEvictionTimeout, util.NewTokenBucketRateLimiter(s.DeletingPodsQps, s.DeletingPodsBurst),
		s.NodeMonitorGracePeriod, s.NodeStartupGracePeriod, s.NodeMonitorPeriod, (*net.IPNet)(&s.ClusterCIDR), s.AllocateNodeCIDRs)
	nodeController.Run(s.NodeSyncPeriod, s.SyncNodeList)

	serviceController := servicecontroller.New(cloud, kubeClient, s.ClusterName)
	if err := serviceController.Run(s.NodeSyncPeriod); err != nil {
		glog.Errorf("Failed to start service controller: %v", err)
	}

	resourceQuotaManager := resourcequota.NewResourceQuotaManager(kubeClient)
	resourceQuotaManager.Run(s.ResourceQuotaSyncPeriod)

	namespaceManager := namespace.NewNamespaceManager(kubeClient, s.NamespaceSyncPeriod)
	namespaceManager.Run()

	pvclaimBinder := volumeclaimbinder.NewPersistentVolumeClaimBinder(kubeClient, s.PVClaimBinderSyncPeriod)
	pvclaimBinder.Run()

	if len(s.ServiceAccountKeyFile) > 0 {
		privateKey, err := serviceaccount.ReadPrivateKey(s.ServiceAccountKeyFile)
		if err != nil {
			glog.Errorf("Error reading key for service account token controller: %v", err)
		} else {
			serviceaccount.NewTokensController(
				kubeClient,
				serviceaccount.DefaultTokenControllerOptions(
					serviceaccount.JWTTokenGenerator(privateKey),
				),
			).Run()
		}
	}

	serviceaccount.NewServiceAccountsController(
		kubeClient,
		serviceaccount.DefaultServiceAccountsControllerOptions(),
	).Run()

	select {}
}

func (s *CMServer) createEndpointController(client *client.Client) kmendpoint.EndpointController {
	if s.UseHostPortEndpoints {
		glog.V(2).Infof("Creating hostIP:hostPort endpoint controller")
		return kmendpoint.NewEndpointController(client)
	}
	glog.V(2).Infof("Creating podIP:containerPort endpoint controller")
	stockEndpointController := kendpoint.NewEndpointController(client)
	return stockEndpointController
}
