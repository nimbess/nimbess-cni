// Copyright (c) 2019 Red Hat and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// TODO: Nimbess CNI plugin needs tests

// Main package for the Nimbess CNI plugin
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ipam"
	"google.golang.org/grpc"

	cnitypes "github.com/containernetworking/cni/pkg/types/current"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
	cninimbess "github.com/nimbess/nimbess-agent/pkg/proto/cni"
	log "github.com/sirupsen/logrus"
)

const defaultLogFile = "/var/log/nimbess/nimbess-cni.log"

// K8sArgs is the valid CNI_ARGS used for Kubernetes
type k8sArgs struct {
	types.CommonArgs
	IP                         net.IP
	K8S_POD_NAME               types.UnmarshallableString
	K8S_POD_NAMESPACE          types.UnmarshallableString
	K8S_POD_INFRA_CONTAINER_ID types.UnmarshallableString
}

// NinbessNetworkConfig describes a network to which a container can be joined
type nimbessNetworkConfig struct {
	// CNIVersion
	CNIVersion string `json:"cniVersion"`
	// Network name that is unique across all containers on the host
	Name string `json:"name"`

	// Filename of CNI plugin executable
	Type string `json:"type"`

	// Additional dictionary arguments provided by container runtime (optional)
	Args map[string]string `json:"Args"`

	// Sets up IP masquerade on the host for this network. Optional.
	IpMasq bool                     `json:"ipMasq"`
	Dns    *cninimbess.CNIReply_DNS `json:"dns"`
}

// NimbessConfig is whatever you expect your configuration json to be. This is whatever
// is passed in on stdin. Your plugin may wish to expose its functionality via
// runtime args, see CONVENTIONS.md in the CNI spec.
type nimbessConfig struct {
	// common CNI config
	types.NetConf

	// PrevResult contains previous plugin's result, used only when called in the context of a chained plugin.
	PrevResult *map[string]interface{} `json:"prevResult"`

	// GrpcServer is a plugin-specific config, contains location of the gRPC server
	// where the CNI requests are being forwarded to (server:port tuple, e.g. "localhost:9111")
	// or unix-domain socket path (e.g. "/run/cni.sock").
	GrpcServer string `json:"grpcServer"`

	// LogFile is a plugin-specific config, specifies location of the CNI plugin log file.
	// If empty, plugin logs into defaultLogFile.
	LogFile string `json:"logFile"`

	// EtcdEndpoints is a plugin-specific config, may contain comma-separated list of ETCD endpoints
	// required for specific for the CNI / IPAM plugin.
	EtcdEndpoints string `json:"etcdEndpoints"`

	// NetworkConfig describes a network to which a container can be joined
	NetworkConfig nimbessNetworkConfig `json:"networkConfig"`

	// IPAM information
	IpamType string `json:"ipamType"`
}

// grpcConnect sets up a connection to the gRPC server specified in grpcServer argument
// as a server:port tuple (e.g. "localhost:9111") or unix-domain socket path (e.g. "/run/cni.sock").
func grpcConnect(grpcServer string) (conn *grpc.ClientConn, cni cninimbess.RemoteCNIClient, err error) {

	if grpcServer != "" && grpcServer[0] == '/' {
		// unix-domain socket connection
		conn, err = grpc.Dial(
			grpcServer,
			grpc.WithInsecure(),
			grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
				return net.DialTimeout("unix", addr, timeout)
			}),
		)
	} else {
		// TCP socket connection
		conn, err = grpc.Dial(grpcServer, grpc.WithInsecure())
	}

	if err != nil {
		return
	}
	cni = cninimbess.NewRemoteCNIClient(conn)
	return
}

// parseCNIConfig parses CNI config from JSON (in bytes) to nimbessConfig struct.
func parseCNIConfig(bytes []byte) (*nimbessConfig, error) {
	// unmarshal the config
	conf := &nimbessConfig{}
	if err := json.Unmarshal(bytes, conf); err != nil {
		return nil, fmt.Errorf("failed to load plugin config: %v", err)
	}

	// CNI chaining is not supported by this plugin, print out an error in case it was chained
	if conf.PrevResult != nil {
		return nil, fmt.Errorf("CNI chaining is not supported by this plugin")
	}

	// grpcServer is mandatory
	if conf.GrpcServer == "" {
		return nil, fmt.Errorf(`"grpcServer" field is required. It specifies where the CNI requests should be forwarded to`)
	}

	return conf, nil
}

// initLog initializes logging into the specified file
func initLog(fileName string) error {
	if fileName == "" {
		fileName = defaultLogFile
	}
	f, err := os.OpenFile(fileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	log.SetOutput(f)
	log.SetLevel(log.DebugLevel)
	return nil
}

// cmdAdd implements the CNI requests to add a container to a network
// It forwards the requests to the remote gRPC server and prints the results received from it.
func cmdAdd(args *skel.CmdArgs) error {
	start := time.Now()

	// Parse the CNI config
	conf, err := parseCNIConfig(args.StdinData)
	if err != nil {
		return err
	}

	// Init the logger
	err = initLog(conf.LogFile)
	if err != nil {
		log.Errorf("Unable to initialize logging: %v", err)
		return err
	}
	// These fields are coming from skel
	log.WithFields(log.Fields{
		"ContainerID": args.ContainerID,
		"Netns":       args.Netns,
		"IfName":      args.IfName,
		"Args":        args.Args,
	}).Debug("CNI ADD request")

	k8Arg := &k8sArgs{}
	if err := types.LoadArgs(args.Args, k8Arg); err != nil {
		log.Errorf("Unable to parse Pod name or namespace: %v", err)
		return err
	}

	// Prepare CNI Request for Network Config
	cniRequestNW := &cninimbess.CNIRequest_NetworkConfig{
		CniVersion: conf.CNIVersion,
		Name:       conf.NetworkConfig.Name,
		Type:       conf.NetworkConfig.Type,
		Args:       conf.NetworkConfig.Args,
		IpMasq:     conf.NetworkConfig.IpMasq,
		Dns:        conf.NetworkConfig.Dns,
	}

	// Prepare CNI request
	cniRequest := &cninimbess.CNIRequest{
		Version:          conf.CNIVersion,
		ContainerId:      args.ContainerID,
		NetworkNamespace: args.Netns,
		InterfaceName:    args.IfName,
		NetworkConfig:    cniRequestNW,
		ExtraArguments:   cniRequestNW.Args,
		IpamType:         conf.IPAM.Type,
		PodName:          string(k8Arg.K8S_POD_NAME),
		PodNamespace:     string(k8Arg.K8S_POD_NAMESPACE),
	}

	cniResult := &cnitypes.Result{
		CNIVersion: conf.CNIVersion,
	}

	// Invoke ipam del if err to avoid ip leak
	defer func() {
		if err != nil {
			ipam.ExecDel(cniRequest.IpamType, args.StdinData)
		}
	}()

	// Run the IPAM plugin and get back the config to apply
	var ipamRequest types.Result
	if cniRequest.IpamType != "nimbess" {
		ipamRequest, err = ipam.ExecAdd(cniRequest.IpamType, args.StdinData)
		if err != nil {
			return err
		}
	}

	// Convert IPAM result received to the correct format for the result version
	ipamResult := &cnitypes.Result{}
	if cniRequest.IpamType != "nimbess" {
		ipamResult, err = cnitypes.NewResultFromResult(ipamRequest)
		if err != nil {
			return err
		}
		if len(ipamResult.IPs) == 0 {
			return errors.New("IPAM plugin returned missing IP config")
		}
	}

	cniResult.IPs = ipamResult.IPs
	cniResult.Routes = ipamResult.Routes
	cniResult.DNS = ipamResult.DNS

	for _, ipc := range cniResult.IPs {
		// All addresses apply to the container Nimbess interface
		ipc.Interface = cnitypes.Int(0)
	}

	// Connect to the remote CNI handler over gRPC
	conn, c, err := grpcConnect(conf.GrpcServer)
	if err != nil {
		log.Errorf("Unable to connect to GRPC server %s: %v", conf.GrpcServer, err)
		return err
	}
	defer conn.Close()

	// Execute the remote ADD request
	r, err := c.Add(context.Background(), cniRequest)
	if err != nil {
		log.Errorf("Error by executing remote CNI Add request: %v", err)
		return err
	}

	// Process interfaces
	for ifidx, iface := range r.Interfaces {
		// append interface info
		cniResult.Interfaces = append(cniResult.Interfaces, &cnitypes.Interface{
			Name:    iface.Name,
			Mac:     iface.Mac,
			Sandbox: iface.Sandbox,
		})
		for _, ip := range iface.IpAddresses {
			// append interface ip address info
			ipAddr, ipNet, err := net.ParseCIDR(ip.Address)
			if err != nil {
				log.Error(err)
				return err
			}
			ipNet.IP = ipAddr
			var gwAddr net.IP
			if ip.Gateway != "" {
				gwAddr = net.ParseIP(ip.Gateway)
				if err != nil {
					log.Error(err)
					return err
				}
			}
			ver := "4"
			if ip.Version == cninimbess.CNIReply_Interface_IP_IPV6 {
				ver = "6"
			}
			cniResult.IPs = append(cniResult.IPs, &cnitypes.IPConfig{
				Address:   *ipNet,
				Version:   ver,
				Interface: &ifidx,
				Gateway:   gwAddr,
			})
		}
	}

	// Process routes
	for _, route := range r.Routes {
		_, dstIP, err := net.ParseCIDR(route.Dst)
		if err != nil {
			log.Error(err)
			return err
		}
		gwAddr := net.ParseIP(route.Gw)
		if err != nil {
			log.Error(err)
			return err
		}
		cniResult.Routes = append(cniResult.Routes, &types.Route{
			Dst: *dstIP,
			GW:  gwAddr,
		})
	}

	// Process DNS entry
	for _, dns := range r.Dns {
		cniResult.DNS.Nameservers = dns.Nameservers
		cniResult.DNS.Domain = dns.Domain
		cniResult.DNS.Search = dns.Search
		cniResult.DNS.Options = dns.Options
	}

	log.WithFields(log.Fields{"Result": cniResult}).Debugf("CNI ADD request OK, took %s", time.Since(start))

	return cniResult.Print()
}

// cmdDel implements the CNI request to delete a container from network.
// It forwards the request to he remote gRPC server and returns the result received from gRPC.
func cmdDel(args *skel.CmdArgs) error {
	start := time.Now()

	// Parse CNI config
	conf, err := parseCNIConfig(args.StdinData)
	if err != nil {
		log.Errorf("Unable to parse CNI config: %v", err)
		return err
	}

	err = initLog(conf.LogFile)
	if err != nil {
		log.Errorf("Unable to initialize logging: %v", err)
		return err
	}
	log.WithFields(log.Fields{
		"ContainerID": args.ContainerID,
		"Netns":       args.Netns,
		"IfName":      args.IfName,
		"Args":        args.Args,
	}).Debug("CNI DEL request")

	// connect to remote CNI handler over gRPC
	conn, c, err := grpcConnect(conf.GrpcServer)
	if err != nil {
		log.Errorf("Unable to connect to GRPC server %s: %v", conf.GrpcServer, err)
		return err
	}
	defer conn.Close()

	// Prepare CNI Request for Network Config
	cniRequestNW := &cninimbess.CNIRequest_NetworkConfig{}

	// Prepare CNI request
	cniRequest := &cninimbess.CNIRequest{
		Version:          conf.CNIVersion,
		ContainerId:      args.ContainerID,
		NetworkNamespace: args.Netns,
		InterfaceName:    args.IfName,
		NetworkConfig:    cniRequestNW,
		ExtraArguments:   cniRequestNW.Args,
	}
	// execute the DELETE request
	_, err = c.Delete(context.Background(), &cninimbess.CNIRequest{
		Version:          conf.CNIVersion,
		ContainerId:      args.ContainerID,
		InterfaceName:    args.IfName,
		NetworkNamespace: args.Netns,
		NetworkConfig:    cniRequestNW,
		ExtraArguments:   cniRequestNW.Args,
	})
	if err != nil {
		log.Errorf("Error by executing remote CNI Delete request: %v", err)
		return err
	}

	err = ipam.ExecDel(cniRequest.IpamType, args.StdinData)
	if err != nil {
		return err
	}

	log.Debugf("CNI DEL request OK, took %s", time.Since(start))

	return nil
}

//TODO: cmdCheck needs to be implemented
//cmdCheck implements the CNI request to check the state of a container connected to a network
func cmdCheck(args *skel.CmdArgs) error {
	return nil
}

// main routine of the CNI plugin
func main() {
	// execute the CNI plugin logic
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, bv.BuildString("nimbess"))
}
