// Copyright (c) 2025 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

// Package configpatch provides a helper to lookup config patches by machine/machine-set.
package configpatch

import (
	"context"

	"github.com/cosi-project/runtime/pkg/controller"

	"github.com/siderolabs/omni/internal/backend/runtime/omni/controllers/omni/internal/configpatch"
)

// Helper provides a way to lookup config patches by machine/machine-set.
type Helper = configpatch.Helper

// NewHelper creates a new config patch helper.
func NewHelper(ctx context.Context, r controller.Reader) (*Helper, error) {
	return configpatch.NewHelper(ctx, r)
}
