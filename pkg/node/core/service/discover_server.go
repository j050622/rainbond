// RAINBOND, Application Management Platform
// Copyright (C) 2014-2017 Goodrain Co., Ltd.

// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. For any non-GPL usage of Rainbond,
// one or multiple Commercial Licenses authorized by Goodrain Co., Ltd.
// must be obtained first.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package service

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/goodrain/rainbond/cmd/node/option"
	api_model "github.com/goodrain/rainbond/pkg/api/model"
	"github.com/goodrain/rainbond/pkg/api/util"
	node_model "github.com/goodrain/rainbond/pkg/node/api/model"
	"github.com/goodrain/rainbond/pkg/node/core/k8s"
	"github.com/goodrain/rainbond/pkg/node/core/store"
	"github.com/pquerna/ffjson/ffjson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/pkg/api/v1"
)

//DiscoverAction DiscoverAction
type DiscoverAction struct {
	conf    *option.Conf
	etcdCli *store.Client
}

//CreateDiscoverActionManager CreateDiscoverActionManager
func CreateDiscoverActionManager(conf *option.Conf) *DiscoverAction {
	return &DiscoverAction{
		conf:    conf,
		etcdCli: store.DefalutClient,
	}
}

//DiscoverService sds
func (d *DiscoverAction) DiscoverService(serviceInfo string) (*node_model.SDS, *util.APIHandleError) {
	mm := strings.Split(serviceInfo, "_")
	if len(mm) < 3 {
		return nil, util.CreateAPIHandleError(400, fmt.Errorf("service_name is not in good format"))
	}
	namespace := mm[0]
	serviceAlias := mm[1]
	dPort := mm[2]
	//deployVersion := mm[3]

	labelname := fmt.Sprintf("name=%sService", serviceAlias)
	endpoints, err := k8s.K8S.Core().Endpoints(namespace).List(metav1.ListOptions{LabelSelector: labelname})
	logrus.Debugf("labelname is %s, endpoints is %v, items is %v", labelname, endpoints, endpoints.Items)
	if err != nil {
		return nil, util.CreateAPIHandleError(500, err)
	}
	services, err := k8s.K8S.Core().Services(namespace).List(metav1.ListOptions{LabelSelector: labelname})
	if err != nil {
		return nil, util.CreateAPIHandleError(500, err)
	}
	if len(endpoints.Items) == 0 {
		return nil, util.CreateAPIHandleError(400, fmt.Errorf("have no endpoints"))
	}
	var sdsL []*node_model.PieceSDS
	for key, item := range endpoints.Items {
		addressList := item.Subsets[0].Addresses
		if len(addressList) == 0 {
			addressList = item.Subsets[0].NotReadyAddresses
		}
		port := item.Subsets[0].Ports[0].Port
		if dPort != fmt.Sprintf("%d", port) {
			continue
		}
		toport := services.Items[key].Spec.Ports[0].Port
		for _, ip := range addressList {
			sdsP := &node_model.PieceSDS{
				IPAddress: ip.IP,
				Port:      toport,
			}
			sdsL = append(sdsL, sdsP)
		}
	}
	sds := &node_model.SDS{
		Hosts: sdsL,
	}
	return sds, nil
}

//DiscoverListeners lds
func (d *DiscoverAction) DiscoverListeners(
	tenantService,
	serviceCluster string) (*node_model.LDS, *util.APIHandleError) {
	nn := strings.Split(tenantService, "_")
	if len(nn) != 2 {
		return nil, util.CreateAPIHandleError(400,
			fmt.Errorf("namesapces and service_alias not in good format"))
	}
	namespace := nn[0]
	serviceAlias := nn[1]
	mm := strings.Split(serviceCluster, "_")
	if len(mm) == 0 {
		return nil, util.CreateAPIHandleError(400, fmt.Errorf("service_name is not in good format"))
	}

	//TODO: console控制尽量不把小于1000的端口给用户使用
	var vhL []*node_model.PieceHTTPVirtualHost
	var ldsL []*node_model.PieceLDS
	for _, destServiceAlias := range mm {
		labelname := fmt.Sprintf("name=%sService", destServiceAlias)
		endpoint, err := k8s.K8S.Core().Endpoints(namespace).List(metav1.ListOptions{LabelSelector: labelname})
		if err != nil {
			return nil, util.CreateAPIHandleError(500, err)
		}
		services, err := k8s.K8S.Core().Services(namespace).List(metav1.ListOptions{LabelSelector: labelname})
		if err != nil {
			return nil, util.CreateAPIHandleError(500, err)
		}
		if len(endpoint.Items) == 0 {
			continue
		}
		for _, service := range services.Items {
			//TODO: HTTP inner的protocol添加资源时需要label
			inner, ok := service.Labels["service_type"]
			if !ok || inner != "inner" {
				continue
			}
			port := service.Spec.Ports[0].Port
			portProtocol, ok := service.Labels["port_protocol"]
			if ok {
				logrus.Debugf("port protocol is %s", portProtocol)
				switch portProtocol {
				case "stream":
					ptr := &node_model.PieceTCPRoute{
						Cluster: fmt.Sprintf("%s_%s_%d", namespace, destServiceAlias, port),
					}
					lrs := &node_model.LDSTCPRoutes{
						Routes: []*node_model.PieceTCPRoute{ptr},
					}
					lcg := &node_model.LDSTCPConfig{
						StatPrefix:  fmt.Sprintf("%s_%s_%d", namespace, destServiceAlias, port),
						RouteConfig: lrs,
					}
					lfs := &node_model.LDSFilters{
						Name:   "tcp_proxy",
						Config: lcg,
					}
					plds := &node_model.PieceLDS{
						Name:    fmt.Sprintf("%s_%s_%d", namespace, destServiceAlias, port),
						Address: fmt.Sprintf("tcp://0.0.0.0:%d", port),
						Filters: []*node_model.LDSFilters{lfs},
					}
					ldsL = append(ldsL, plds)
					continue
				case "http":
					envName := fmt.Sprintf("%s_%d", destServiceAlias, port)
					ss, err := d.GetSourcesEnv(namespace, "downStream", envName)
					if err != nil {
						logrus.Warnf("get env %s error, %v", envName, err)
						continue
					}
					var sr api_model.NetDownStreamRules
					if err := ffjson.Unmarshal([]byte(ss.SourceBody.EnvVal), &sr); err != nil {
						logrus.Warnf("unmashal downstream rules error, %v", err)
						continue
					}
					prs := &node_model.PieceHTTPRoutes{
						TimeoutMS: 0,
						Prefix:    d.ToolsGetRouterItem(destServiceAlias, node_model.PREFIX, &sr).(string),
						Cluster:   fmt.Sprintf("%s_%s_%d", namespace, destServiceAlias, port),
						Headers: d.ToolsGetRouterItem(destServiceAlias,
							node_model.HEADERS, &sr).([]*node_model.PieceHeader),
					}
					pvh := &node_model.PieceHTTPVirtualHost{
						Name:    fmt.Sprintf("%s_%s_%d", namespace, destServiceAlias, port),
						Domains: d.ToolsGetRouterItem(destServiceAlias, node_model.DOMAINS, &sr).([]string),
						Routes:  []*node_model.PieceHTTPRoutes{prs},
					}
					vhL = append(vhL, pvh)
					continue
				default:
					continue
				}
			}
		}
	}
	if len(vhL) != 0 {
		httpPort, err := d.ToolsGetHTTPPort(namespace, "downStream", serviceAlias)
		if err != nil {
			logrus.Errorf("get http port error, %v", err)
			return nil, util.CreateAPIHandleError(500, err)
		}
		hsf := &node_model.HTTPSingleFileter{
			Type:   "decoder",
			Name:   "router",
			Config: make(map[string]string),
		}
		lhc := &node_model.LDSHTTPConfig{
			CodecType:  "auto",
			StatPrefix: "ingress_http",
			//RouteConfig: rcg,
			Filters: []*node_model.HTTPSingleFileter{hsf},
		}
		lfs := &node_model.LDSFilters{
			Name:   "http_connection_manager",
			Config: lhc,
		}
		plds := &node_model.PieceLDS{
			Name:    fmt.Sprintf("%s_%s_http_80", namespace, serviceAlias),
			Address: fmt.Sprintf("tcp://0.0.0.0:%d", httpPort),
			Filters: []*node_model.LDSFilters{lfs},
		}
		//修改http-port console 完成
		ldsL = append(ldsL, plds)
	}
	lds := &node_model.LDS{
		Listeners: ldsL,
	}
	return lds, nil
}

//DiscoverClusters cds
func (d *DiscoverAction) DiscoverClusters(
	tenantService,
	serviceCluster string) (*node_model.CDS, *util.APIHandleError) {
	nn := strings.Split(tenantService, "_")
	if len(nn) != 2 {
		return nil, util.CreateAPIHandleError(400, fmt.Errorf("namesapces and service_alias not in good format"))
	}
	namespace := nn[0]
	//serviceAlias := nn[1]
	mm := strings.Split(serviceCluster, "_")
	if len(mm) == 0 {
		return nil, util.CreateAPIHandleError(400, fmt.Errorf("service_name is not in good format"))
	}
	var cdsL []*node_model.PieceCDS
	for _, destServiceAlias := range mm {
		labelname := fmt.Sprintf("name=%sService", destServiceAlias)
		services, err := k8s.K8S.Core().Services(namespace).List(metav1.ListOptions{LabelSelector: labelname})
		if err != nil {
			return nil, util.CreateAPIHandleError(500, err)
		}
		for _, service := range services.Items {
			inner, ok := service.Labels["service_type"]
			if !ok || inner != "inner" {
				continue
			}
			port := service.Spec.Ports[0]
			envName := fmt.Sprintf("%s_%d", destServiceAlias, port.Port)
			ss, err := d.GetSourcesEnv(namespace, "downStream", envName)
			if err != nil {
				logrus.Warnf("get env %s error, %v", envName, err)
				continue
			}
			var sr api_model.NetDownStreamRules
			if err := ffjson.Unmarshal([]byte(ss.SourceBody.EnvVal), &sr); err != nil {
				logrus.Warnf("unmashal downstream rules error, %v", err)
				continue
			}
			circuits := d.ToolsGetRouterItem(destServiceAlias, node_model.LIMITS, &sr).(int)
			cb := &node_model.CircuitBreakers{
				Default: &node_model.MaxConnections{
					MaxConnections: circuits,
				},
			}
			pcds := &node_model.PieceCDS{
				Name:             fmt.Sprintf("%s_%s_%v", namespace, destServiceAlias, port.Port),
				Type:             "sds",
				ConnectTimeoutMS: 250,
				LBType:           "round_robin",
				ServiceName:      fmt.Sprintf("%s_%s_%v", namespace, destServiceAlias, port.Port),
				CircuitBreakers:  cb,
			}
			cdsL = append(cdsL, pcds)
			continue
		}
	}
	cds := &node_model.CDS{
		Clusters: cdsL,
	}
	return cds, nil
}

//GetSourcesEnv rds
func (d *DiscoverAction) GetSourcesEnv(namespace, sourceAlias, envName string) (*api_model.SourceSpec, *util.APIHandleError) {
	k := fmt.Sprintf("/sources/define/%s/%s/%s", namespace, sourceAlias, envName)
	resp, err := d.etcdCli.Get(k)
	if err != nil {
		logrus.Errorf("get etcd value error, %v", err)
		return nil, util.CreateAPIHandleError(500, err)
	}
	var ss api_model.SourceSpec
	if resp.Count != 0 {
		v := resp.Kvs[0].Value
		if err := ffjson.Unmarshal(v, &ss); err != nil {
			logrus.Errorf("unmashal etcd v error, %v", err)
			return nil, util.CreateAPIHandleError(500, err)
		}
		return &ss, nil
	}
	return &ss, nil
}

//ToolsGetK8SServiceList GetK8SServiceList
func (d *DiscoverAction) ToolsGetK8SServiceList(uuid string) (*v1.ServiceList, error) {
	serviceList, err := k8s.K8S.Core().Services(uuid).List(metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return serviceList, nil
}

//ToolsGetMainPodEnvs ToolsGetMainPodEnvs
func (d *DiscoverAction) ToolsGetMainPodEnvs(namespace, serviceAlias string) (
	*[]v1.EnvVar,
	*util.APIHandleError) {
	labelname := fmt.Sprintf("name=%s", serviceAlias)
	pods, err := k8s.K8S.Core().Pods(namespace).List(metav1.ListOptions{LabelSelector: labelname})
	logrus.Debugf("service_alias %s pod is %v", serviceAlias, pods)
	if err != nil {
		return nil, util.CreateAPIHandleError(500, err)
	}
	if len(pods.Items) == 0 {
		return nil,
			util.CreateAPIHandleError(404, fmt.Errorf("have no pod for discover"))
	}
	if len(pods.Items[0].Spec.Containers) < 2 {
		return nil,
			util.CreateAPIHandleError(404, fmt.Errorf("have no net plugins for discover"))
	}
	for _, c := range pods.Items[0].Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "PLUGIN_MOEL" && strings.Contains(e.Value, "net-plugin") {
				return &c.Env, nil
			}
		}
	}
	return nil, util.CreateAPIHandleError(404, fmt.Errorf("have no envs for plugin"))
}

//ToolsBuildPieceLDS ToolsBuildPieceLDS
func (d *DiscoverAction) ToolsBuildPieceLDS() {}

//ToolsGetRouterItem ToolsGetRouterItem
func (d *DiscoverAction) ToolsGetRouterItem(destAlias, kind string, sr *api_model.NetDownStreamRules) interface{} {
	switch kind {
	case node_model.PREFIX:
		if sr.Prefix != "" {
			return sr.Prefix
		}
		return "/"
	case node_model.LIMITS:
		if sr.Limit != 0 {
			return sr.Limit
		}
		return 1024
	case node_model.HEADERS:
		var phL []*node_model.PieceHeader
		if sr.Header != nil {
			for _, h := range sr.Header {
				ph := &node_model.PieceHeader{
					Name:  h.Key,
					Value: h.Value,
				}
				phL = append(phL, ph)
			}
		}
		ph := &node_model.PieceHeader{
			Name:  "host",
			Value: sr.ServiceAlias,
		}
		phL = append(phL, ph)
		return phL
	case node_model.DOMAINS:
		if sr.Domain != nil {
			return sr.Domain
		}
		return []string{sr.ServiceAlias}
	}
	return ""
}

//ToolsGetEnvValue ToolsGetEnvValue
func ToolsGetEnvValue(ename string, envs *[]v1.EnvVar) string {
	for _, e := range *envs {
		if e.Name == ename {
			return e.Value
		}
	}
	return ""
}

//ToolsGetHTTPPort ToolsGetHTTPPort
func (d *DiscoverAction) ToolsGetHTTPPort(namespace, sourceAlias, serviceAlias string) (int, error) {
	key := fmt.Sprintf("/sources/define/%s/%s/%s_http_port", namespace, sourceAlias, serviceAlias)
	resp, err := d.etcdCli.Get(key)
	if err != nil {
		logrus.Errorf("get service %s http port error, %v", serviceAlias, err)
		return 0, err
	}
	if resp.Count != 0 {
		v := resp.Kvs[0].Value
		port, err := strconv.Atoi(string(v))
		if err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, nil
}
