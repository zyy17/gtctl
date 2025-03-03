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

package playground

import (
	"github.com/lucasepe/codename"
	"github.com/spf13/cobra"

	"github.com/GreptimeTeam/gtctl/pkg/cmd/gtctl/cluster/create"
	"github.com/GreptimeTeam/gtctl/pkg/logger"
)

func NewPlaygroundCommand(l logger.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "playground",
		Short: "Starts a GreptimeDB cluster playground",
		Long:  "Starts a GreptimeDB cluster playground in bare-metal",
		RunE: func(cmd *cobra.Command, args []string) error {
			rng, err := codename.DefaultRNG()
			if err != nil {
				return nil
			}

			playgroundName := codename.Generate(rng, 0)
			playgroundOptions := create.ClusterCliOptions{
				BareMetal:      true,
				RetainLogs:     false,
				Timeout:        900, // 15min
				AlwaysDownload: false,
			}

			if err := create.NewCluster([]string{playgroundName}, playgroundOptions, l); err != nil {
				return err
			}
			return nil
		},
	}
}
