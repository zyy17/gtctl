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

package create

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GreptimeTeam/gtctl/pkg/cmd/gtctl/cluster/common"
	"github.com/GreptimeTeam/gtctl/pkg/deployer"
	"github.com/GreptimeTeam/gtctl/pkg/deployer/baremetal"
	"github.com/GreptimeTeam/gtctl/pkg/deployer/k8s"
	"github.com/GreptimeTeam/gtctl/pkg/logger"
	"github.com/GreptimeTeam/gtctl/pkg/status"
)

type createClusterCliOptions struct {
	// The options for deploying GreptimeDBCluster in K8s.
	Namespace                      string
	OperatorNamespace              string
	EtcdNamespace                  string
	StorageClassName               string
	StorageSize                    string
	StorageRetainPolicy            string
	GreptimeDBChartVersion         string
	GreptimeDBOperatorChartVersion string
	ImageRegistry                  string
	EtcdEndpoint                   string
	EtcdChartVersion               string
	EtcdStorageClassName           string
	EtcdStorageSize                string

	// The options for deploying GreptimeDBCluster in bare-metal.
	BareMetal          bool
	Config             string
	GreptimeBinVersion string

	// Common options.
	Timeout int
	DryRun  bool
}

func NewCreateClusterCommand(l logger.Logger) *cobra.Command {
	var options createClusterCliOptions

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a GreptimeDB cluster",
		Long:  `Create a GreptimeDB cluster`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("cluster name should be set")
			}

			var (
				clusterName = args[0]

				// TODO(zyy17): should use timeout context.
				ctx = context.TODO()
			)

			clusterDeployer, err := newDeployer(l, clusterName, &options)
			if err != nil {
				return err
			}

			spinner, err := status.NewSpinner()
			if err != nil {
				return err
			}

			if !options.BareMetal {
				l.V(0).Infof("Creating GreptimeDB cluster '%s' in namespace '%s' ...", logger.Bold(clusterName), logger.Bold(options.Namespace))
			} else {
				l.V(0).Infof("Creating GreptimeDB cluster '%s' on bare-metal envrionment...", logger.Bold(clusterName))
			}

			if !options.BareMetal {
				if err := deployGreptimeDBOperator(ctx, l, &options, spinner, clusterDeployer); err != nil {
					return err
				}
			}

			if err := deployEtcdCluster(ctx, l, &options, spinner, clusterDeployer, clusterName); err != nil {
				spinner.Stop(false, "Installing etcd cluster failed")
				return err
			}

			if err := deployGreptimeDBCluster(ctx, l, &options, spinner, clusterDeployer, clusterName); err != nil {
				return err
			}

			if !options.DryRun {
				printTips(l, clusterName, &options)
			}

			if options.BareMetal {
				d, ok := clusterDeployer.(*baremetal.Deployer)
				if ok {
					fmt.Printf("\x1b[32m%s\x1b[0m", fmt.Sprintf("The cluster(pid=%d, version=%s) is running in bare-metal mode now...\n",
						os.Getpid(), d.Config().Cluster.Artifact.Version))
					// Wait for all the child processes to exit.
					if err := d.Wait(ctx); err != nil {
						return err
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&options.OperatorNamespace, "operator-namespace", "default", "The namespace of deploying greptimedb-operator.")
	cmd.Flags().StringVar(&options.StorageClassName, "storage-class-name", "standard", "Datanode storage class name.")
	cmd.Flags().StringVar(&options.StorageSize, "storage-size", "10Gi", "Datanode persistent volume size.")
	cmd.Flags().StringVar(&options.StorageRetainPolicy, "retain-policy", "Retain", "Datanode pvc retain policy.")
	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "default", "Namespace of GreptimeDB cluster.")
	cmd.Flags().BoolVar(&options.DryRun, "dry-run", false, "Output the manifests without applying them.")
	cmd.Flags().IntVar(&options.Timeout, "timeout", -1, "Timeout in seconds for the command to complete, default is no timeout.")
	cmd.Flags().StringVar(&options.GreptimeDBChartVersion, "greptimedb-chart-version", "", "The greptimedb helm chart version, use latest version if not specified.")
	cmd.Flags().StringVar(&options.GreptimeDBOperatorChartVersion, "greptimedb-operator-chart-version", "", "The greptimedb-operator helm chart version, use latest version if not specified.")
	cmd.Flags().StringVar(&options.EtcdChartVersion, "etcd-chart-version", "", "The greptimedb-etcd helm chart version, use latest version if not specified.")
	cmd.Flags().StringVar(&options.ImageRegistry, "image-registry", "", "The image registry")
	cmd.Flags().StringVar(&options.EtcdNamespace, "etcd-namespace", "default", "The namespace of etcd cluster.")
	cmd.Flags().StringVar(&options.EtcdStorageClassName, "etcd-storage-class-name", "standard", "The etcd storage class name.")
	cmd.Flags().StringVar(&options.EtcdStorageSize, "etcd-storage-size", "10Gi", "the etcd persistent volume size.")
	cmd.Flags().BoolVar(&options.BareMetal, "bare-metal", false, "Deploy the greptimedb cluster on bare-metal envrionment.")
	cmd.Flags().StringVar(&options.GreptimeBinVersion, "greptime-bin-version", "", "The version of greptime binary(can be override by config file).")
	cmd.Flags().StringVar(&options.Config, "config", "", "Configuration to deploy the greptimedb cluster on bare-metal envrionment.")

	return cmd
}

func newDeployer(l logger.Logger, clusterName string, options *createClusterCliOptions) (deployer.Interface, error) {
	if !options.BareMetal {
		k8sDeployer, err := k8s.NewDeployer(l, k8s.WithDryRun(options.DryRun),
			k8s.WithTimeout(time.Duration(options.Timeout)*time.Second))
		if err != nil {
			return nil, err
		}
		return k8sDeployer, nil
	}

	var opts []baremetal.Option

	if options.GreptimeBinVersion != "" {
		opts = append(opts, baremetal.WithGreptimeVersion(options.GreptimeBinVersion))
	}

	if options.Config != "" {
		var config baremetal.Config
		data, err := ioutil.ReadFile(options.Config)
		if err != nil {
			return nil, err
		}

		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, err
		}

		opts = append(opts, baremetal.WithConfig(&config))
	}

	baremetalDeployer, err := baremetal.NewDeployer(l, clusterName, opts...)
	if err != nil {
		return nil, err
	}

	return baremetalDeployer, nil
}

func deployGreptimeDBOperator(ctx context.Context, l logger.Logger, options *createClusterCliOptions,
	spinner *status.Spinner, clusterDeployer deployer.Interface) error {

	spinner.Start("Installing greptimedb-operator...")
	createGreptimeDBOperatorOptions := &deployer.CreateGreptimeDBOperatorOptions{
		GreptimeDBOperatorChartVersion: options.GreptimeDBOperatorChartVersion,
		ImageRegistry:                  options.ImageRegistry,
	}
	name := types.NamespacedName{Namespace: options.Namespace, Name: "greptimedb-operator"}.String()
	if err := clusterDeployer.CreateGreptimeDBOperator(ctx, name, createGreptimeDBOperatorOptions); err != nil {
		spinner.Stop(false, "Installing greptimedb-operator failed")
		return err
	}
	spinner.Stop(true, "Installing greptimedb-operator successfully 🎉")

	return nil
}

func deployEtcdCluster(ctx context.Context, l logger.Logger, options *createClusterCliOptions,
	spinner *status.Spinner, clusterDeployer deployer.Interface, clusterName string) error {

	spinner.Start("Installing etcd cluster...")
	createEtcdClusterOptions := &deployer.CreateEtcdClusterOptions{
		ImageRegistry:        options.ImageRegistry,
		EtcdChartVersion:     options.EtcdChartVersion,
		EtcdStorageClassName: options.EtcdStorageClassName,
		EtcdStorageSize:      options.EtcdStorageSize,
	}

	var name string
	if options.BareMetal {
		name = clusterName
	} else {
		name = types.NamespacedName{Namespace: options.EtcdNamespace, Name: common.EtcdClusterName(clusterName)}.String()
	}

	if err := clusterDeployer.CreateEtcdCluster(ctx, name, createEtcdClusterOptions); err != nil {
		spinner.Stop(false, "Installing etcd cluster failed")
		return err
	}
	spinner.Stop(true, "Installing etcd cluster successfully 🎉")

	return nil
}

func deployGreptimeDBCluster(ctx context.Context, l logger.Logger, options *createClusterCliOptions,
	spinner *status.Spinner, clusterDeployer deployer.Interface, clusterName string) error {

	spinner.Start("Installing GreptimeDB cluster...")
	createGreptimeDBClusterOptions := &deployer.CreateGreptimeDBClusterOptions{
		GreptimeDBChartVersion: options.GreptimeDBChartVersion,
		ImageRegistry:          options.ImageRegistry,
		EtcdEndPoint:           fmt.Sprintf("%s.%s:2379", common.EtcdClusterName(clusterName), options.EtcdNamespace),
	}

	var name string
	if options.BareMetal {
		name = clusterName
	} else {
		name = types.NamespacedName{Namespace: options.Namespace, Name: clusterName}.String()
	}

	if err := clusterDeployer.CreateGreptimeDBCluster(ctx, name, createGreptimeDBClusterOptions); err != nil {
		spinner.Stop(false, "Installing GreptimeDB cluster failed")
		return err
	}
	spinner.Stop(true, "Installing GreptimeDB cluster successfully 🎉")

	return nil
}

func printTips(l logger.Logger, clusterName string, options *createClusterCliOptions) {
	l.V(0).Infof("\nNow you can use the following commands to access the GreptimeDB cluster:")
	l.V(0).Infof("\n%s", logger.Bold("MySQL >"))
	if !options.BareMetal {
		l.V(0).Infof("%s", fmt.Sprintf("%s kubectl port-forward svc/%s-frontend -n %s 4002:4002 > connections-mysql.out &", logger.Bold("$"), clusterName, options.Namespace))
	}
	l.V(0).Infof("%s", fmt.Sprintf("%s mysql -h 127.0.0.1 -P 4002", logger.Bold("$")))
	l.V(0).Infof("\n%s", logger.Bold("PostgreSQL >"))
	if !options.BareMetal {
		l.V(0).Infof("%s", fmt.Sprintf("%s kubectl port-forward svc/%s-frontend -n %s 4003:4003 > connections-pg.out &", logger.Bold("$"), clusterName, options.Namespace))
	}
	l.V(0).Infof("%s", fmt.Sprintf("%s psql -h 127.0.0.1 -p 4003", logger.Bold("$")))
	l.V(0).Infof("\nThank you for using %s! Check for more information on %s. 😊", logger.Bold("GreptimeDB"), logger.Bold("https://greptime.com"))
	l.V(0).Infof("\n%s 🔑", logger.Bold("Invest in Data, Harvest over Time."))
}
