// Copyright 2023 Greptime Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8s

import (
	"bytes"
	"context"
	"fmt"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"os"
	"path"
	"strings"
	"time"

	greptimedbclusterv1alpha1 "github.com/GreptimeTeam/greptimedb-operator/apis/v1alpha1"
	"gopkg.in/yaml.v3"
	helmKube "helm.sh/helm/v3/pkg/kube"

	. "github.com/GreptimeTeam/gtctl/pkg/deployer"
	"github.com/GreptimeTeam/gtctl/pkg/deployer/baremetal/config"
	"github.com/GreptimeTeam/gtctl/pkg/helm"
	"github.com/GreptimeTeam/gtctl/pkg/kube"
	"github.com/GreptimeTeam/gtctl/pkg/logger"
	fileutils "github.com/GreptimeTeam/gtctl/pkg/utils/file"
)

type deployer struct {
	helmManager    *helm.Manager
	client         *kube.Client
	helmKubeClient *helmKube.Client
	timeout        time.Duration
	logger         logger.Logger
	dryRun         bool
}

type deploymentMeta struct {
	Values                 helm.Values `yaml:"values"`
	Chart                  string      `yaml:"chart"`
	Version                string      `yaml:"version"`
	Name                   string      `yaml:"name"`
	Namespace              string      `yaml:"namespace"`
	Service                string      `yaml:"service"`
	UseGreptimeCNArtifacts bool        `yaml:"useGreptimeCNArtifacts"`
	Manifests              string      `yaml:"manifests"`
}

const (
	AliCloudRegistry = "greptime-registry.cn-hangzhou.cr.aliyuncs.com"
)

var _ Interface = &deployer{}

type Option func(*deployer)

func NewDeployer(l logger.Logger, opts ...Option) (Interface, error) {
	hm, err := helm.NewManager(l)
	if err != nil {
		return nil, err
	}

	d := &deployer{
		helmManager: hm,
		logger:      l,
	}

	for _, opt := range opts {
		opt(d)
	}

	var client *kube.Client
	if !d.dryRun {
		client, err = kube.NewClient("")
		if err != nil {
			return nil, err
		}
	}

	helmKubeClient := helmKube.New(&genericclioptions.ConfigFlags{})
	if err := helmKubeClient.IsReachable(); err != nil {
		return nil, err
	}

	d.client = client
	d.helmKubeClient = helmKubeClient

	return d, nil
}

func WithDryRun(dryRun bool) Option {
	return func(d *deployer) {
		d.dryRun = dryRun
	}
}

func WithTimeout(timeout time.Duration) Option {
	return func(d *deployer) {
		d.timeout = timeout
	}
}

func (d *deployer) GetGreptimeDBCluster(ctx context.Context, name string, options *GetGreptimeDBClusterOptions) (*GreptimeDBCluster, error) {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return nil, err
	}

	cluster, err := d.client.GetCluster(ctx, resourceName, resourceNamespace)
	if err != nil {
		return nil, err
	}

	return &GreptimeDBCluster{
		Raw: cluster,
	}, nil
}

func (d *deployer) ListGreptimeDBClusters(ctx context.Context, options *ListGreptimeDBClustersOptions) ([]*GreptimeDBCluster, error) {
	clusters, err := d.client.ListClusters(ctx)
	if err != nil {
		return nil, err
	}

	var result []*GreptimeDBCluster
	for _, cluster := range clusters.Items {
		result = append(result, &GreptimeDBCluster{
			Raw: &cluster,
		})
	}

	return result, nil
}

func (d *deployer) CreateGreptimeDBCluster(ctx context.Context, name string, options *CreateGreptimeDBClusterOptions) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	if options.UseGreptimeCNArtifacts && options.ImageRegistry == "" {
		options.ConfigValues += fmt.Sprintf("image.registry=%s,initializer.registry=%s,", AliCloudRegistry, AliCloudRegistry)
	}

	helmValues, err := helm.ToHelmValues(*options, "")
	if err != nil {
		return err
	}

	manifests, err := d.helmManager.LoadAndRenderChart(ctx, resourceName, resourceNamespace, helm.GreptimeDBChartName, options.GreptimeDBChartVersion, options.UseGreptimeCNArtifacts, helmValues)
	if err != nil {
		return err
	}

	if d.dryRun {
		d.logger.V(0).Info(string(manifests))
		return nil
	}

	dm := &deploymentMeta{
		Values:                 helmValues,
		Chart:                  helm.GreptimeDBChartName,
		Version:                options.GreptimeDBChartVersion,
		Namespace:              resourceNamespace,
		Name:                   resourceName,
		Service:                "greptimedb-cluster",
		UseGreptimeCNArtifacts: options.UseGreptimeCNArtifacts,
		Manifests:              string(manifests),
	}
	if err := d.storeDeploymentMeta(dm); err != nil {
		return err
	}

	resources, err := d.helmKubeClient.Build(bytes.NewBuffer(manifests), false)
	if err != nil {
		return err
	}

	_, err = d.helmKubeClient.Create(resources)
	if err != nil {
		return err
	}

	return d.helmKubeClient.Wait(resources, d.timeout)
}

func (d *deployer) UpdateGreptimeDBCluster(ctx context.Context, name string, options *UpdateGreptimeDBClusterOptions) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	newCluster, ok := options.NewCluster.Raw.(*greptimedbclusterv1alpha1.GreptimeDBCluster)
	if !ok {
		return fmt.Errorf("invalid cluster type")
	}

	if err := d.client.UpdateCluster(ctx, resourceNamespace, newCluster); err != nil {
		return err
	}

	return d.client.WaitForClusterReady(ctx, resourceName, resourceNamespace, d.timeout)
}

func (d *deployer) DeleteGreptimeDBCluster(ctx context.Context, name string, options *DeleteGreptimeDBClusterOption) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	dm, err := d.getDeploymentMeta("greptimedb-cluster", resourceNamespace, resourceName)
	if err != nil {
		return err
	}

	resources, err := d.helmKubeClient.Build(bytes.NewBufferString(dm.Manifests), false)
	if err != nil {
		return err
	}

	_, errors := d.helmKubeClient.Delete(resources)
	if len(errors) != 0 {
		return fmt.Errorf("error while deleting cluster: %v", errors)
	}

	return d.helmKubeClient.WaitForDelete(resources, 10*time.Minute)
}

func (d *deployer) CreateEtcdCluster(ctx context.Context, name string, options *CreateEtcdClusterOptions) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	// TODO(zyy17): Maybe we can set this in the top level configures.
	const (
		disableRBACConfig = "auth.rbac.create=false,auth.rbac.token.enabled=false,"
	)
	options.ConfigValues += disableRBACConfig

	if options.UseGreptimeCNArtifacts && options.ImageRegistry == "" {
		options.ConfigValues += fmt.Sprintf("image.registry=%s,", AliCloudRegistry)
	}

	helmValues, err := helm.ToHelmValues(*options, "")
	if err != nil {
		return err
	}

	dm, err := d.getDeploymentMeta("etcd", resourceNamespace, resourceName)
	if dm != nil { // If the cluster already exists, we should update it.
		return nil
	}

	manifests, err := d.helmManager.LoadAndRenderChart(ctx, resourceName, resourceNamespace, helm.EtcdBitnamiOCIRegistry, helm.DefaultEtcdChartVersion, options.UseGreptimeCNArtifacts, helmValues)
	if err != nil {
		return fmt.Errorf("error while loading helm chart: %v", err)
	}

	if d.dryRun {
		d.logger.V(0).Info(string(manifests))
		return nil
	}

	dm = &deploymentMeta{
		Values:                 helmValues,
		Chart:                  helm.EtcdBitnamiOCIRegistry,
		Version:                helm.DefaultEtcdChartVersion,
		Namespace:              resourceNamespace,
		Name:                   resourceName,
		Service:                "etcd",
		UseGreptimeCNArtifacts: options.UseGreptimeCNArtifacts,
		Manifests:              string(manifests),
	}
	if err := d.storeDeploymentMeta(dm); err != nil {
		return err
	}

	resources, err := d.helmKubeClient.Build(bytes.NewBuffer(manifests), false)
	if err != nil {
		return err
	}

	_, err = d.helmKubeClient.Create(resources)
	if err != nil {
		return err
	}

	return d.helmKubeClient.Wait(resources, d.timeout)
}

func (d *deployer) DeleteEtcdCluster(ctx context.Context, name string, options *DeleteEtcdClusterOption) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	dm, err := d.getDeploymentMeta("etcd", resourceNamespace, resourceName)
	if err != nil {
		return err
	}

	resources, err := d.helmKubeClient.Build(bytes.NewBufferString(dm.Manifests), false)
	if err != nil {
		return err
	}

	_, errors := d.helmKubeClient.Delete(resources)
	if len(errors) != 0 {
		return fmt.Errorf("error while deleting cluster: %v", errors)
	}

	return d.helmKubeClient.WaitForDelete(resources, 10*time.Minute)
}

func (d *deployer) CreateGreptimeDBOperator(ctx context.Context, name string, options *CreateGreptimeDBOperatorOptions) error {
	resourceNamespace, resourceName, err := d.splitNamescapedName(name)
	if err != nil {
		return err
	}

	if options.UseGreptimeCNArtifacts && options.ImageRegistry == "" {
		options.ConfigValues += fmt.Sprintf("image.registry=%s,", AliCloudRegistry)
	}

	helmValues, err := helm.ToHelmValues(*options, "")
	if err != nil {
		return err
	}

	dm, err := d.getDeploymentMeta("greptimedb-operator", resourceNamespace, resourceName)
	if dm != nil { // If the cluster already exists, we should update it.
		return nil
	}

	manifests, err := d.helmManager.LoadAndRenderChart(ctx, resourceName, resourceNamespace, helm.GreptimeDBOperatorChartName, options.GreptimeDBOperatorChartVersion, options.UseGreptimeCNArtifacts, helmValues)
	if err != nil {
		return err
	}

	if d.dryRun {
		d.logger.V(0).Info(string(manifests))
		return nil
	}

	dm = &deploymentMeta{
		Values:                 helmValues,
		Chart:                  helm.GreptimeDBOperatorChartName,
		Version:                options.GreptimeDBOperatorChartVersion,
		Namespace:              resourceNamespace,
		Name:                   resourceName,
		Service:                "greptimedb-operator",
		UseGreptimeCNArtifacts: options.UseGreptimeCNArtifacts,
		Manifests:              string(manifests),
	}
	if err := d.storeDeploymentMeta(dm); err != nil {
		return err
	}

	resources, err := d.helmKubeClient.Build(bytes.NewBuffer(manifests), false)
	if err != nil {
		return err
	}

	_, err = d.helmKubeClient.Create(resources)
	if err != nil {
		return err
	}

	return d.helmKubeClient.Wait(resources, d.timeout)
}

func (d *deployer) splitNamescapedName(name string) (string, string, error) {
	if name == "" {
		return "", "", fmt.Errorf("empty namespaced name")
	}

	split := strings.Split(name, "/")
	if len(split) != 2 {
		return "", "", fmt.Errorf("invalid namespaced name '%s'", name)
	}

	return split[0], split[1], nil
}

func (d *deployer) storeDeploymentMeta(dm *deploymentMeta) error {
	data, err := yaml.Marshal(dm)
	if err != nil {
		return err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	valuesFile := path.Join(homeDir, config.GtctlDir, dm.Service, fmt.Sprintf("%s_%s", dm.Namespace, dm.Name), "deployment.yaml")

	if err := fileutils.CreateDirIfNotExists(path.Dir(valuesFile)); err != nil {
		return err
	}

	f, err := os.Create(valuesFile)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}

	return nil
}

func (d *deployer) getDeploymentMeta(service, namespace, name string) (*deploymentMeta, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	valuesFile := path.Join(homeDir, config.GtctlDir, service, fmt.Sprintf("%s_%s", namespace, name), "deployment.yaml")

	data, err := os.ReadFile(valuesFile)
	if err != nil {
		return nil, err
	}

	var dm deploymentMeta
	if err := yaml.Unmarshal(data, &dm); err != nil {
		return nil, err
	}

	return &dm, nil
}
