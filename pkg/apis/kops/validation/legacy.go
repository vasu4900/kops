/*
Copyright 2019 The Kubernetes Authors.

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

package validation

import (
	"fmt"
	"net"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kops/pkg/apis/kops"
	"k8s.io/kops/pkg/apis/kops/util"
	"k8s.io/kops/pkg/featureflag"
	"k8s.io/kops/pkg/model/components"
	"k8s.io/kops/pkg/util/subnet"
	"k8s.io/kops/upup/pkg/fi"

	"github.com/blang/semver"
)

// legacy contains validation functions that don't match the apimachinery style

// ValidateCluster is responsible for checking the validity of the Cluster spec
func ValidateCluster(c *kops.Cluster, strict bool) field.ErrorList {
	fieldSpec := field.NewPath("spec")
	allErrs := field.ErrorList{}

	// kubernetesRelease is the version with only major & minor fields
	// We initialize to an arbitrary value, preferably in the supported range,
	// in case the value in c.Spec.KubernetesVersion is blank or unparseable.
	kubernetesRelease := semver.Version{Major: 1, Minor: 15}

	// KubernetesVersion
	if c.Spec.KubernetesVersion == "" {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubernetesVersion"), ""))
	} else {
		sv, err := util.ParseKubernetesVersion(c.Spec.KubernetesVersion)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubernetesVersion"), c.Spec.KubernetesVersion, "unable to determine kubernetes version"))
		} else {
			kubernetesRelease = semver.Version{Major: sv.Major, Minor: sv.Minor}
		}
	}

	if c.ObjectMeta.Name == "" {
		allErrs = append(allErrs, field.Required(field.NewPath("Name"), "Cluster Name is required (e.g. --name=mycluster.myzone.com)"))
	} else {
		// Must be a dns name
		errs := validation.IsDNS1123Subdomain(c.ObjectMeta.Name)
		if len(errs) != 0 {
			allErrs = append(allErrs, field.Invalid(field.NewPath("Name"), c.ObjectMeta.Name, fmt.Sprintf("Cluster Name must be a valid DNS name (e.g. --name=mycluster.myzone.com) errors: %s", strings.Join(errs, ", "))))
		} else if !strings.Contains(c.ObjectMeta.Name, ".") {
			// Tolerate if this is a cluster we are importing for upgrade
			if c.ObjectMeta.Annotations[kops.AnnotationNameManagement] != kops.AnnotationValueManagementImported {
				allErrs = append(allErrs, field.Invalid(field.NewPath("Name"), c.ObjectMeta.Name, "Cluster Name must be a fully-qualified DNS name (e.g. --name=mycluster.myzone.com)"))
			}
		}
	}

	if c.Spec.Assets != nil && c.Spec.Assets.ContainerProxy != nil && c.Spec.Assets.ContainerRegistry != nil {
		allErrs = append(allErrs, field.Forbidden(fieldSpec.Child("Assets", "ContainerProxy"), "ContainerProxy cannot be used in conjunction with ContainerRegistry as represent mutually exclusive concepts. Please consult the documentation for details."))
	}

	requiresSubnets := true
	requiresNetworkCIDR := true
	requiresSubnetCIDR := true
	switch kops.CloudProviderID(c.Spec.CloudProvider) {
	case "":
		allErrs = append(allErrs, field.Required(fieldSpec.Child("CloudProvider"), ""))
		requiresSubnets = false
		requiresSubnetCIDR = false
		requiresNetworkCIDR = false

	case kops.CloudProviderBareMetal:
		requiresSubnets = false
		requiresSubnetCIDR = false
		requiresNetworkCIDR = false
		if c.Spec.NetworkCIDR != "" {
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NetworkCIDR"), c.Spec.NetworkCIDR, "NetworkCIDR should not be set on bare metal"))
		}

	case kops.CloudProviderGCE:
		requiresNetworkCIDR = false
		if c.Spec.NetworkCIDR != "" {
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NetworkCIDR"), c.Spec.NetworkCIDR, "NetworkCIDR should not be set on GCE"))
		}
		requiresSubnetCIDR = false

	case kops.CloudProviderDO:
		requiresSubnets = false
		requiresSubnetCIDR = false
		requiresNetworkCIDR = false
		if c.Spec.NetworkCIDR != "" {
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NetworkCIDR"), c.Spec.NetworkCIDR, "NetworkCIDR should not be set on DigitalOcean"))
		}
	case kops.CloudProviderALI:
		requiresSubnets = false
		requiresSubnetCIDR = false
		requiresNetworkCIDR = false
	case kops.CloudProviderAWS:
	case kops.CloudProviderVSphere:
	case kops.CloudProviderOpenstack:
		requiresNetworkCIDR = false
		requiresSubnetCIDR = false

	default:
		allErrs = append(allErrs, field.Invalid(fieldSpec.Child("CloudProvider"), c.Spec.CloudProvider, "CloudProvider not recognized"))
	}

	if requiresSubnets && len(c.Spec.Subnets) == 0 {
		// TODO: Auto choose zones from region?
		allErrs = append(allErrs, field.Required(fieldSpec.Child("Subnets"), "must configure at least one Subnet (use --zones)"))
	}

	if strict && c.Spec.Kubelet == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("Kubelet"), "Kubelet not configured"))
	}
	if strict && c.Spec.MasterKubelet == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("MasterKubelet"), "MasterKubelet not configured"))
	}
	if strict && c.Spec.KubeControllerManager == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeControllerManager"), "KubeControllerManager not configured"))
	}
	if strict && c.Spec.KubeDNS == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeDNS"), "KubeDNS not configured"))
	}
	if strict && c.Spec.KubeScheduler == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeScheduler"), "KubeScheduler not configured"))
	}
	if strict && c.Spec.KubeAPIServer == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeAPIServer"), "KubeAPIServer not configured"))
	}
	if strict && c.Spec.KubeProxy == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeProxy"), "KubeProxy not configured"))
	}
	if strict && c.Spec.Docker == nil {
		allErrs = append(allErrs, field.Required(fieldSpec.Child("Docker"), "Docker not configured"))
	}

	// Check NetworkCIDR
	var networkCIDR *net.IPNet
	var err error
	{
		if c.Spec.NetworkCIDR == "" {
			if requiresNetworkCIDR {
				allErrs = append(allErrs, field.Required(fieldSpec.Child("NetworkCIDR"), "Cluster did not have NetworkCIDR set"))
			}
		} else {
			_, networkCIDR, err = net.ParseCIDR(c.Spec.NetworkCIDR)
			if err != nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NetworkCIDR"), c.Spec.NetworkCIDR, fmt.Sprintf("Cluster had an invalid NetworkCIDR")))
			}
		}
	}

	// Check AdditionalNetworkCIDRs
	var additionalNetworkCIDRs []*net.IPNet
	{
		if len(c.Spec.AdditionalNetworkCIDRs) > 0 {
			for _, AdditionalNetworkCIDR := range c.Spec.AdditionalNetworkCIDRs {
				_, IPNetAdditionalNetworkCIDR, err := net.ParseCIDR(AdditionalNetworkCIDR)
				if err != nil {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("AdditionalNetworkCIDRs"), AdditionalNetworkCIDR, fmt.Sprintf("Cluster had an invalid AdditionalNetworkCIDRs")))
				}
				additionalNetworkCIDRs = append(additionalNetworkCIDRs, IPNetAdditionalNetworkCIDR)
			}
		}
	}

	// nonMasqueradeCIDR is essentially deprecated, and we're moving to cluster-cidr instead (which is better named pod-cidr)
	nonMasqueradeCIDRRequired := true
	serviceClusterMustBeSubnetOfNonMasqueradeCIDR := true
	if c.Spec.Networking != nil && c.Spec.Networking.GCE != nil {
		nonMasqueradeCIDRRequired = false
		serviceClusterMustBeSubnetOfNonMasqueradeCIDR = false
	}

	// Check NonMasqueradeCIDR
	var nonMasqueradeCIDR *net.IPNet
	{
		nonMasqueradeCIDRString := c.Spec.NonMasqueradeCIDR
		if nonMasqueradeCIDRString == "" {
			if nonMasqueradeCIDRRequired {
				allErrs = append(allErrs, field.Required(fieldSpec.Child("NonMasqueradeCIDR"), "Cluster did not have NonMasqueradeCIDR set"))
			}
		} else {
			_, nonMasqueradeCIDR, err = net.ParseCIDR(nonMasqueradeCIDRString)
			if err != nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NonMasqueradeCIDR"), nonMasqueradeCIDRString, "Cluster had an invalid NonMasqueradeCIDR"))
			}

			if networkCIDR != nil && subnet.Overlap(nonMasqueradeCIDR, networkCIDR) && c.Spec.Networking != nil && c.Spec.Networking.AmazonVPC == nil && c.Spec.Networking.LyftVPC == nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NonMasqueradeCIDR"), nonMasqueradeCIDRString, fmt.Sprintf("NonMasqueradeCIDR %q cannot overlap with NetworkCIDR %q", nonMasqueradeCIDRString, c.Spec.NetworkCIDR)))
			}

			if c.Spec.Kubelet != nil && c.Spec.Kubelet.NonMasqueradeCIDR != nonMasqueradeCIDRString {
				if strict || c.Spec.Kubelet.NonMasqueradeCIDR != "" {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NonMasqueradeCIDR"), nonMasqueradeCIDRString, "Kubelet NonMasqueradeCIDR did not match cluster NonMasqueradeCIDR"))
				}
			}
			if c.Spec.MasterKubelet != nil && c.Spec.MasterKubelet.NonMasqueradeCIDR != nonMasqueradeCIDRString {
				if strict || c.Spec.MasterKubelet.NonMasqueradeCIDR != "" {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("NonMasqueradeCIDR"), nonMasqueradeCIDRString, "MasterKubelet NonMasqueradeCIDR did not match cluster NonMasqueradeCIDR"))
				}
			}
		}
	}

	// Check ServiceClusterIPRange
	var serviceClusterIPRange *net.IPNet
	{
		serviceClusterIPRangeString := c.Spec.ServiceClusterIPRange
		if serviceClusterIPRangeString == "" {
			if strict {
				allErrs = append(allErrs, field.Required(fieldSpec.Child("ServiceClusterIPRange"), "Cluster did not have ServiceClusterIPRange set"))
			}
		} else {
			_, serviceClusterIPRange, err = net.ParseCIDR(serviceClusterIPRangeString)
			if err != nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("ServiceClusterIPRange"), serviceClusterIPRangeString, "Cluster had an invalid ServiceClusterIPRange"))
			} else {
				if nonMasqueradeCIDR != nil && serviceClusterMustBeSubnetOfNonMasqueradeCIDR && !subnet.BelongsTo(nonMasqueradeCIDR, serviceClusterIPRange) {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("ServiceClusterIPRange"), serviceClusterIPRangeString, fmt.Sprintf("ServiceClusterIPRange %q must be a subnet of NonMasqueradeCIDR %q", serviceClusterIPRangeString, c.Spec.NonMasqueradeCIDR)))
				}

				if c.Spec.KubeAPIServer != nil && c.Spec.KubeAPIServer.ServiceClusterIPRange != serviceClusterIPRangeString {
					if strict || c.Spec.KubeAPIServer.ServiceClusterIPRange != "" {
						allErrs = append(allErrs, field.Invalid(fieldSpec.Child("ServiceClusterIPRange"), serviceClusterIPRangeString, "KubeAPIServer ServiceClusterIPRange did not match cluster ServiceClusterIPRange"))
					}
				}
			}
		}
	}

	// Check Canal Networking Spec if used
	if c.Spec.Networking != nil && c.Spec.Networking.Canal != nil {
		action := c.Spec.Networking.Canal.DefaultEndpointToHostAction
		switch action {
		case "", "ACCEPT", "DROP", "RETURN":
		default:
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Networking", "Canal", "DefaultEndpointToHostAction"), action, fmt.Sprintf("Unsupported value: %s, supports 'ACCEPT', 'DROP' or 'RETURN'", action)))
		}

		chainInsertMode := c.Spec.Networking.Canal.ChainInsertMode
		switch chainInsertMode {
		case "", "insert", "append":
		default:
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Networking", "Canal", "ChainInsertMode"), chainInsertMode, fmt.Sprintf("Unsupported value: %s, supports 'insert' or 'append'", chainInsertMode)))
		}

		logSeveritySys := c.Spec.Networking.Canal.LogSeveritySys
		switch logSeveritySys {
		case "", "INFO", "DEBUG", "WARNING", "ERROR", "CRITICAL", "NONE":
		default:
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Networking", "Canal", "LogSeveritySys"), logSeveritySys, fmt.Sprintf("Unsupported value: %s, supports 'INFO', 'DEBUG', 'WARNING', 'ERROR', 'CRITICAL' or 'NONE'", logSeveritySys)))
		}
	}

	// Check ClusterCIDR
	if c.Spec.KubeControllerManager != nil {
		var clusterCIDR *net.IPNet
		clusterCIDRString := c.Spec.KubeControllerManager.ClusterCIDR
		if clusterCIDRString != "" {
			_, clusterCIDR, err = net.ParseCIDR(clusterCIDRString)
			if err != nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubeControllerManager", "ClusterCIDR"), clusterCIDRString, "Cluster had an invalid KubeControllerManager.ClusterCIDR"))
			} else if nonMasqueradeCIDR != nil && !subnet.BelongsTo(nonMasqueradeCIDR, clusterCIDR) {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubeControllerManager", "ClusterCIDR"), clusterCIDRString, fmt.Sprintf("KubeControllerManager.ClusterCIDR %q must be a subnet of NonMasqueradeCIDR %q", clusterCIDRString, c.Spec.NonMasqueradeCIDR)))
			}
		}
	}

	// @check the custom kubedns options are valid
	if c.Spec.KubeDNS != nil {
		if c.Spec.KubeDNS.ServerIP != "" {
			address := c.Spec.KubeDNS.ServerIP
			ip := net.ParseIP(address)
			if ip == nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "serverIP"), address, "Cluster had an invalid kubeDNS.serverIP"))
			} else {
				if serviceClusterIPRange != nil && !serviceClusterIPRange.Contains(ip) {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "serverIP"), address, fmt.Sprintf("ServiceClusterIPRange %q must contain the DNS Server IP %q", c.Spec.ServiceClusterIPRange, address)))
				}
				if !featureflag.ExperimentalClusterDNS.Enabled() {
					if c.Spec.Kubelet != nil && c.Spec.Kubelet.ClusterDNS != c.Spec.KubeDNS.ServerIP {
						allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "serverIP"), address, "Kubelet ClusterDNS did not match cluster kubeDNS.serverIP"))
					}
					if c.Spec.MasterKubelet != nil && c.Spec.MasterKubelet.ClusterDNS != c.Spec.KubeDNS.ServerIP {
						allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "serverIP"), address, "MasterKubelet ClusterDNS did not match cluster kubeDNS.serverIP"))
					}
				}
			}
		}

		// @check the nameservers are valid
		for i, x := range c.Spec.KubeDNS.UpstreamNameservers {
			if ip := net.ParseIP(x); ip == nil {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "upstreamNameservers").Index(i), x, "Invalid nameserver given, should be a valid ip address"))
			}
		}

		// @check the stubdomain if any
		for domain, nameservers := range c.Spec.KubeDNS.StubDomains {
			if len(nameservers) <= 0 {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "stubDomains").Key(domain), domain, "No nameservers specified for the stub domain"))
			}
			for i, x := range nameservers {
				if ip := net.ParseIP(x); ip == nil {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("kubeDNS", "stubDomains").Key(domain).Index(i), x, "Invalid nameserver given, should be a valid ip address"))
				}
			}
		}
	}

	// Check CloudProvider
	{

		var k8sCloudProvider string
		switch kops.CloudProviderID(c.Spec.CloudProvider) {
		case kops.CloudProviderAWS:
			k8sCloudProvider = "aws"
		case kops.CloudProviderGCE:
			k8sCloudProvider = "gce"
		case kops.CloudProviderDO:
			k8sCloudProvider = "external"
		case kops.CloudProviderVSphere:
			k8sCloudProvider = "vsphere"
		case kops.CloudProviderBareMetal:
			k8sCloudProvider = ""
		case kops.CloudProviderOpenstack:
			k8sCloudProvider = "openstack"
		case kops.CloudProviderALI:
			k8sCloudProvider = "alicloud"
		default:
			// We already added an error above
			k8sCloudProvider = "ignore"
		}

		if k8sCloudProvider != "ignore" {
			if c.Spec.Kubelet != nil && (strict || c.Spec.Kubelet.CloudProvider != "") {
				if c.Spec.Kubelet.CloudProvider != "external" && k8sCloudProvider != c.Spec.Kubelet.CloudProvider {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Kubelet", "CloudProvider"), c.Spec.Kubelet.CloudProvider, "Did not match cluster CloudProvider"))
				}
			}
			if c.Spec.MasterKubelet != nil && (strict || c.Spec.MasterKubelet.CloudProvider != "") {
				if c.Spec.MasterKubelet.CloudProvider != "external" && k8sCloudProvider != c.Spec.MasterKubelet.CloudProvider {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("MasterKubelet", "CloudProvider"), c.Spec.MasterKubelet.CloudProvider, "Did not match cluster CloudProvider"))

				}
			}
			if c.Spec.KubeAPIServer != nil && (strict || c.Spec.KubeAPIServer.CloudProvider != "") {
				if c.Spec.KubeAPIServer.CloudProvider != "external" && k8sCloudProvider != c.Spec.KubeAPIServer.CloudProvider {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubeAPIServer", "CloudProvider"), c.Spec.KubeAPIServer.CloudProvider, "Did not match cluster CloudProvider"))
				}
			}
			if c.Spec.KubeControllerManager != nil && (strict || c.Spec.KubeControllerManager.CloudProvider != "") {
				if c.Spec.KubeControllerManager.CloudProvider != "external" && k8sCloudProvider != c.Spec.KubeControllerManager.CloudProvider {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubeControllerManager", "CloudProvider"), c.Spec.KubeControllerManager.CloudProvider, "Did not match cluster CloudProvider"))
				}
			}
		}
	}

	// Check that the subnet CIDRs are all consistent
	{
		for i, s := range c.Spec.Subnets {
			fieldSubnet := fieldSpec.Child("Subnets").Index(i)
			if s.CIDR == "" {
				if requiresSubnetCIDR && strict {
					allErrs = append(allErrs, field.Required(fieldSubnet.Child("CIDR"), "Subnet did not have a CIDR set"))
				}
			} else {
				_, subnetCIDR, err := net.ParseCIDR(s.CIDR)
				if err != nil {
					allErrs = append(allErrs, field.Invalid(fieldSubnet.Child("CIDR"), s.CIDR, "Subnet had an invalid CIDR"))
				} else if networkCIDR != nil && !validateSubnetCIDR(networkCIDR, additionalNetworkCIDRs, subnetCIDR) {
					allErrs = append(allErrs, field.Invalid(fieldSubnet.Child("CIDR"), s.CIDR, fmt.Sprintf("Subnet %q had a CIDR %q that was not a subnet of the NetworkCIDR %q", s.Name, s.CIDR, c.Spec.NetworkCIDR)))
				}
			}
		}
	}

	// NodeAuthorization
	if c.Spec.NodeAuthorization != nil {
		// @check the feature gate is enabled for this
		if !featureflag.EnableNodeAuthorization.Enabled() {
			allErrs = append(allErrs, field.Invalid(field.NewPath("nodeAuthorization"), nil, "node authorization is experimental feature; set `export KOPS_FEATURE_FLAGS=EnableNodeAuthorization`"))
		} else {
			if c.Spec.NodeAuthorization.NodeAuthorizer == nil {
				allErrs = append(allErrs, field.Invalid(field.NewPath("nodeAuthorization"), nil, "no node authorization policy has been set"))
			} else {
				path := field.NewPath("nodeAuthorization").Child("nodeAuthorizer")
				if c.Spec.NodeAuthorization.NodeAuthorizer.Port < 0 || c.Spec.NodeAuthorization.NodeAuthorizer.Port >= 65535 {
					allErrs = append(allErrs, field.Invalid(path.Child("port"), c.Spec.NodeAuthorization.NodeAuthorizer.Port, "invalid port"))
				}
				if c.Spec.NodeAuthorization.NodeAuthorizer.Timeout != nil && c.Spec.NodeAuthorization.NodeAuthorizer.Timeout.Duration <= 0 {
					allErrs = append(allErrs, field.Invalid(path.Child("timeout"), c.Spec.NodeAuthorization.NodeAuthorizer.Timeout, "must be greater than zero"))
				}
				if c.Spec.NodeAuthorization.NodeAuthorizer.TokenTTL != nil && c.Spec.NodeAuthorization.NodeAuthorizer.TokenTTL.Duration < 0 {
					allErrs = append(allErrs, field.Invalid(path.Child("tokenTTL"), c.Spec.NodeAuthorization.NodeAuthorizer.TokenTTL, "must be greater than or equal to zero"))
				}

				// @question: we could probably just default these settings in the model when the node-authorizer is enabled??
				if c.Spec.KubeAPIServer == nil {
					allErrs = append(allErrs, field.Invalid(field.NewPath("kubeAPIServer"), c.Spec.KubeAPIServer, "bootstrap token authentication is not enabled in the kube-apiserver"))
				} else if c.Spec.KubeAPIServer.EnableBootstrapAuthToken == nil {
					allErrs = append(allErrs, field.Invalid(field.NewPath("kubeAPIServer").Child("enableBootstrapAuthToken"), nil, "kube-apiserver has not been configured to use bootstrap tokens"))
				} else if !fi.BoolValue(c.Spec.KubeAPIServer.EnableBootstrapAuthToken) {
					allErrs = append(allErrs, field.Invalid(field.NewPath("kubeAPIServer").Child("enableBootstrapAuthToken"),
						c.Spec.KubeAPIServer.EnableBootstrapAuthToken, "bootstrap tokens in the kube-apiserver has been disabled"))
				}
			}
		}
	}

	// UpdatePolicy
	if c.Spec.UpdatePolicy != nil {
		switch *c.Spec.UpdatePolicy {
		case kops.UpdatePolicyExternal:
		// Valid
		default:
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("UpdatePolicy"), *c.Spec.UpdatePolicy, "unrecognized value for UpdatePolicy"))
		}
	}

	// KubeProxy
	if c.Spec.KubeProxy != nil {
		kubeProxyPath := fieldSpec.Child("KubeProxy")
		master := c.Spec.KubeProxy.Master

		for i, x := range c.Spec.KubeProxy.IPVSExcludeCIDRS {
			if _, _, err := net.ParseCIDR(x); err != nil {
				allErrs = append(allErrs, field.Invalid(kubeProxyPath.Child("ipvsExcludeCIDRS").Index(i), x, "Invalid network CIDR"))
			}
		}

		if master != "" && !isValidAPIServersURL(master) {
			allErrs = append(allErrs, field.Invalid(kubeProxyPath.Child("Master"), master, "Not a valid APIServer URL"))
		}
	}

	// KubeAPIServer
	if c.Spec.KubeAPIServer != nil {
		if kubernetesRelease.GTE(semver.MustParse("1.10.0")) {
			if len(c.Spec.KubeAPIServer.AdmissionControl) > 0 {
				if len(c.Spec.KubeAPIServer.DisableAdmissionPlugins) > 0 {
					allErrs = append(allErrs, field.Invalid(fieldSpec.Child("KubeAPIServer").Child("DisableAdmissionPlugins"),
						strings.Join(c.Spec.KubeAPIServer.DisableAdmissionPlugins, ","),
						"DisableAdmissionPlugins is mutually exclusive, you cannot use both AdmissionControl and DisableAdmissionPlugins together"))
				}
			}
		}
	}

	// Kubelet
	if c.Spec.Kubelet != nil {
		kubeletPath := fieldSpec.Child("Kubelet")

		{
			// Flag removed in 1.6
			if c.Spec.Kubelet.APIServers != "" {
				allErrs = append(allErrs, field.Invalid(
					kubeletPath.Child("APIServers"),
					c.Spec.Kubelet.APIServers,
					"api-servers flag was removed in 1.6"))
			}
		}

		if kubernetesRelease.GTE(semver.MustParse("1.10.0")) {
			// Flag removed in 1.10
			if c.Spec.Kubelet.RequireKubeconfig != nil {
				allErrs = append(allErrs, field.Invalid(
					kubeletPath.Child("requireKubeconfig"),
					*c.Spec.Kubelet.RequireKubeconfig,
					"require-kubeconfig flag was removed in 1.10.  (Please be sure you are not using a cluster config from `kops get cluster --full`)"))
			}
		}

		if c.Spec.Kubelet.BootstrapKubeconfig != "" {
			if c.Spec.KubeAPIServer == nil {
				allErrs = append(allErrs, field.Required(fieldSpec.Child("KubeAPIServer"), "bootstrap token require the NodeRestriction admissions controller"))
			}
		}

		if c.Spec.Kubelet.APIServers != "" && !isValidAPIServersURL(c.Spec.Kubelet.APIServers) {
			allErrs = append(allErrs, field.Invalid(kubeletPath.Child("APIServers"), c.Spec.Kubelet.APIServers, "Not a valid APIServer URL"))
		}
	}

	// MasterKubelet
	if c.Spec.MasterKubelet != nil {
		masterKubeletPath := fieldSpec.Child("MasterKubelet")

		{
			// Flag removed in 1.6
			if c.Spec.MasterKubelet.APIServers != "" {
				allErrs = append(allErrs, field.Invalid(
					masterKubeletPath.Child("APIServers"),
					c.Spec.MasterKubelet.APIServers,
					"api-servers flag was removed in 1.6"))
			}
		}

		if kubernetesRelease.GTE(semver.MustParse("1.10.0")) {
			// Flag removed in 1.10
			if c.Spec.MasterKubelet.RequireKubeconfig != nil {
				allErrs = append(allErrs, field.Invalid(
					masterKubeletPath.Child("requireKubeconfig"),
					*c.Spec.MasterKubelet.RequireKubeconfig,
					"require-kubeconfig flag was removed in 1.10.  (Please be sure you are not using a cluster config from `kops get cluster --full`)"))
			}
		}

		if c.Spec.MasterKubelet.APIServers != "" && !isValidAPIServersURL(c.Spec.MasterKubelet.APIServers) {
			allErrs = append(allErrs, field.Invalid(masterKubeletPath.Child("APIServers"), c.Spec.MasterKubelet.APIServers, "Not a valid APIServer URL"))
		}
	}

	// Topology support
	if c.Spec.Topology != nil {
		if c.Spec.Topology.Masters != "" && c.Spec.Topology.Nodes != "" {
			if c.Spec.Topology.Masters != kops.TopologyPublic && c.Spec.Topology.Masters != kops.TopologyPrivate {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Topology", "Masters"), c.Spec.Topology.Masters, "Invalid Masters value for Topology"))
			}
			if c.Spec.Topology.Nodes != kops.TopologyPublic && c.Spec.Topology.Nodes != kops.TopologyPrivate {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Topology", "Nodes"), c.Spec.Topology.Nodes, "Invalid Nodes value for Topology"))
			}

		} else {
			allErrs = append(allErrs, field.Required(fieldSpec.Child("Masters"), "Topology requires non-nil values for Masters and Nodes"))
		}
		if c.Spec.Topology.Bastion != nil {
			bastion := c.Spec.Topology.Bastion
			if c.Spec.Topology.Masters == kops.TopologyPublic || c.Spec.Topology.Nodes == kops.TopologyPublic {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Topology", "Masters"), c.Spec.Topology.Masters, "Bastion supports only Private Masters and Nodes"))
			}
			if bastion.IdleTimeoutSeconds != nil && *bastion.IdleTimeoutSeconds <= 0 {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Topology", "Bastion", "IdleTimeoutSeconds"), *bastion.IdleTimeoutSeconds, "Bastion IdleTimeoutSeconds should be greater than zero"))
			}
			if bastion.IdleTimeoutSeconds != nil && *bastion.IdleTimeoutSeconds > 3600 {
				allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Topology", "Bastion", "IdleTimeoutSeconds"), *bastion.IdleTimeoutSeconds, "Bastion IdleTimeoutSeconds cannot be greater than one hour"))
			}

		}
	}
	// Egress specification support
	{
		for i, s := range c.Spec.Subnets {
			if s.Egress == "" {
				continue
			}
			fieldSubnet := fieldSpec.Child("Subnets").Index(i)
			if !strings.HasPrefix(s.Egress, "nat-") && !strings.HasPrefix(s.Egress, "i-") && s.Egress != kops.EgressExternal {
				allErrs = append(allErrs, field.Invalid(fieldSubnet.Child("Egress"), s.Egress, "egress must be of type NAT Gateway or NAT EC2 Instance or 'External'"))
			}
			if s.Egress != kops.EgressExternal && s.Type != "Private" {
				allErrs = append(allErrs, field.Invalid(fieldSubnet.Child("Egress"), s.Egress, "egress can only be specified for Private subnets"))
			}
		}
	}

	// Etcd
	{
		fieldEtcdClusters := fieldSpec.Child("EtcdClusters")

		if len(c.Spec.EtcdClusters) == 0 {
			allErrs = append(allErrs, field.Required(fieldEtcdClusters, ""))
		} else {
			for i, x := range c.Spec.EtcdClusters {
				allErrs = append(allErrs, validateEtcdClusterSpecLegacy(x, fieldEtcdClusters.Index(i))...)
			}
			allErrs = append(allErrs, validateEtcdTLS(c.Spec.EtcdClusters, fieldEtcdClusters)...)
			allErrs = append(allErrs, validateEtcdStorage(c.Spec.EtcdClusters, fieldEtcdClusters)...)
		}
	}

	{
		if c.Spec.Networking != nil && c.Spec.Networking.Classic != nil {
			allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Networking"), "classic", "classic networking is not supported with kubernetes versions 1.4 and later"))
		}
	}

	if c.Spec.Networking != nil && (c.Spec.Networking.AmazonVPC != nil || c.Spec.Networking.LyftVPC != nil) &&
		c.Spec.CloudProvider != "aws" {
		allErrs = append(allErrs, field.Invalid(fieldSpec.Child("Networking"), "amazon-vpc-routed-eni", "amazon-vpc-routed-eni networking is supported only in AWS"))
	}

	allErrs = append(allErrs, newValidateCluster(c)...)

	if c.Spec.Networking != nil && c.Spec.Networking.Cilium != nil && c.Spec.Networking.Cilium.EnableNodePort && c.Spec.KubeProxy != nil && *c.Spec.KubeProxy.Enabled {
		allErrs = append(allErrs, field.Forbidden(fieldSpec.Child("KubeProxy").Child("enabled"), "When Cilium NodePort is enabled, KubeProxy must be disabled"))
	}

	return allErrs
}

// validateSubnetCIDR is responsible for validating subnets are part of the CIDRs assigned to the cluster.
func validateSubnetCIDR(networkCIDR *net.IPNet, additionalNetworkCIDRs []*net.IPNet, subnetCIDR *net.IPNet) bool {
	if subnet.BelongsTo(networkCIDR, subnetCIDR) {
		return true
	}

	for _, additionalNetworkCIDR := range additionalNetworkCIDRs {
		if subnet.BelongsTo(additionalNetworkCIDR, subnetCIDR) {
			return true
		}
	}

	return false
}

// validateEtcdClusterSpecLegacy is responsible for validating the etcd cluster spec
func validateEtcdClusterSpecLegacy(spec *kops.EtcdClusterSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if spec.Name == "" {
		allErrs = append(allErrs, field.Required(fieldPath.Child("Name"), "EtcdCluster did not have name"))
	}
	if len(spec.Members) == 0 {
		allErrs = append(allErrs, field.Required(fieldPath.Child("Members"), "No members defined in etcd cluster"))
	} else if (len(spec.Members) % 2) == 0 {
		// Not technically a requirement, but doesn't really make sense to allow
		allErrs = append(allErrs, field.Invalid(fieldPath.Child("Members"), len(spec.Members), "Should be an odd number of master-zones for quorum. Use --zones and --master-zones to declare node zones and master zones separately"))
	}
	allErrs = append(allErrs, validateEtcdVersion(spec, fieldPath, nil)...)
	for _, m := range spec.Members {
		allErrs = append(allErrs, validateEtcdMemberSpec(m, fieldPath)...)
	}

	return allErrs
}

// validateEtcdTLS checks the TLS settings for etcd are valid
func validateEtcdTLS(specs []*kops.EtcdClusterSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	var usingTLS int
	for _, x := range specs {
		if x.EnableEtcdTLS {
			usingTLS++
		}
	}
	// check both clusters are using tls if one us enabled
	if usingTLS > 0 && usingTLS != len(specs) {
		allErrs = append(allErrs, field.Invalid(fieldPath.Index(0).Child("EnableEtcdTLS"), false, "Both etcd clusters must have TLS enabled or none at all"))
	}

	return allErrs
}

// validateEtcdStorage is responsible for checks version are identical
func validateEtcdStorage(specs []*kops.EtcdClusterSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	version := specs[0].Version
	for i, x := range specs {
		if x.Version != "" && x.Version != version {
			allErrs = append(allErrs, field.Invalid(fieldPath.Index(i).Child("Version"), x.Version, fmt.Sprintf("cluster: %q, has a different storage versions: %q, both must be the same", x.Name, x.Version)))
		}
	}

	return allErrs
}

// validateEtcdVersion is responsible for validating the storage version of etcd
// @TODO semvar package doesn't appear to ignore a 'v' in v1.1.1 should could be a problem later down the line
func validateEtcdVersion(spec *kops.EtcdClusterSpec, fieldPath *field.Path, minimalVersion *semver.Version) field.ErrorList {
	// @check if the storage is specified, that's is valid

	if minimalVersion == nil {
		v := semver.MustParse("0.0.0")
		minimalVersion = &v
	}

	version := spec.Version
	if spec.Version == "" {
		version = components.DefaultEtcd2Version
	}

	sem, err := semver.Parse(strings.TrimPrefix(version, "v"))
	if err != nil {
		return field.ErrorList{field.Invalid(fieldPath.Child("Version"), version, "the storage version is invalid")}
	}

	// we only support v3 and v2 for now
	if sem.Major == 3 || sem.Major == 2 {
		if sem.LT(*minimalVersion) {
			return field.ErrorList{field.Invalid(fieldPath.Child("Version"), version, fmt.Sprintf("minimal version required is %s", minimalVersion.String()))}
		}
		return nil
	}

	return field.ErrorList{field.Invalid(fieldPath.Child("Version"), version, "unsupported storage version, we only support major versions 2 and 3")}
}

// validateEtcdMemberSpec is responsible for validate the cluster member
func validateEtcdMemberSpec(spec *kops.EtcdMemberSpec, fieldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}
	if spec.Name == "" {
		allErrs = append(allErrs, field.Required(fieldPath.Child("Name"), "EtcdMember did not have Name"))
	}

	if fi.StringValue(spec.InstanceGroup) == "" {
		allErrs = append(allErrs, field.Required(fieldPath.Child("InstanceGroup"), "EtcdMember did not have InstanceGroup"))
	}

	return allErrs
}

// DeepValidate is responsible for validating the instancegroups within the cluster spec
func DeepValidate(c *kops.Cluster, groups []*kops.InstanceGroup, strict bool) error {
	if errs := ValidateCluster(c, strict); len(errs) != 0 {
		return errs.ToAggregate()
	}

	if len(groups) == 0 {
		return fmt.Errorf("must configure at least one InstanceGroup")
	}

	masterGroupCount := 0
	nodeGroupCount := 0
	for _, g := range groups {
		if g.IsMaster() {
			masterGroupCount++
		} else {
			nodeGroupCount++
		}
	}

	if masterGroupCount == 0 {
		return fmt.Errorf("must configure at least one Master InstanceGroup")
	}

	if nodeGroupCount == 0 {
		return fmt.Errorf("must configure at least one Node InstanceGroup")
	}

	for _, g := range groups {
		errs := CrossValidateInstanceGroup(g, c, strict)

		// Additional cloud-specific validation rules,
		// such as making sure that identifiers match the expected formats for the given cloud
		switch kops.CloudProviderID(c.Spec.CloudProvider) {
		case kops.CloudProviderAWS:
			errs = append(errs, awsValidateInstanceGroup(g)...)
		default:
			if len(g.Spec.Volumes) > 0 {
				errs = append(errs, field.Forbidden(field.NewPath("spec.volumes"), "instancegroup volumes are only available with aws at present"))
			}
		}

		if len(errs) != 0 {
			return errs.ToAggregate()
		}
	}

	return nil
}
