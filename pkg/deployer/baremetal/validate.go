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

package baremetal

import (
	"fmt"

	"github.com/go-playground/validator/v10"

	bmconfig "github.com/GreptimeTeam/gtctl/pkg/deployer/baremetal/config"
)

var validate *validator.Validate

// ValidateConfig validate config in bare-metal mode.
func ValidateConfig(config *bmconfig.Config) error {
	if config == nil {
		return fmt.Errorf("no config to validate")
	}

	validate = validator.New()

	// Register custom validation method for Artifact.
	validate.RegisterStructValidation(ValidateArtifact, bmconfig.Artifact{})

	err := validate.Struct(config)
	if err != nil {
		return err
	}

	return nil
}

func ValidateArtifact(sl validator.StructLevel) {
	artifact := sl.Current().Interface().(bmconfig.Artifact)
	if len(artifact.Version) == 0 && len(artifact.Local) == 0 {
		sl.ReportError(sl.Current().Interface(), "Artifact", "Version/Local", "", "")
	}
}
