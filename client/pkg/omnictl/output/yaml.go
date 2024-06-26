// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package output

import (
	"fmt"
	"os"
	"strings"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/meta"
	"github.com/cosi-project/runtime/pkg/state"
	yaml "gopkg.in/yaml.v3"
)

// YAML outputs resources in YAML format.
type YAML struct {
	needDashes bool
	withEvents bool
}

// NewYAML initializes YAML resource output.
func NewYAML() *YAML {
	return &YAML{}
}

// WriteHeader implements output.Writer interface.
func (y *YAML) WriteHeader(_ *meta.ResourceDefinition, withEvents bool) error {
	y.withEvents = withEvents

	return nil
}

// WriteResource implements output.Writer interface.
func (y *YAML) WriteResource(r resource.Resource, event state.EventType) error {
	out, err := resource.MarshalYAML(r)
	if err != nil {
		return err
	}

	if y.needDashes {
		fmt.Fprintln(os.Stdout, "---") //nolint:errcheck
	}

	y.needDashes = true

	if y.withEvents {
		fmt.Fprintf(os.Stdout, "event: %s\n", strings.ToLower(event.String())) //nolint:errcheck
	}

	return yaml.NewEncoder(os.Stdout).Encode(out)
}

// Flush implements output.Writer interface.
func (y *YAML) Flush() error {
	return nil
}
