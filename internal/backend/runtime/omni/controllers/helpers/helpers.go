// Copyright (c) 2025 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

// Package helpers contains common utility methods for COSI controllers of Omni.
package helpers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/cosi-project/runtime/pkg/controller"
	"github.com/cosi-project/runtime/pkg/controller/generic"
	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/resource/kvutils"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	"github.com/siderolabs/gen/xslices"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"

	"github.com/siderolabs/omni/client/pkg/omni/resources"
	"github.com/siderolabs/omni/client/pkg/omni/resources/omni"
	"github.com/siderolabs/omni/internal/backend/runtime/talos"
)

// InputResourceVersionAnnotation is the annotation name where the inputs version sha is stored.
const InputResourceVersionAnnotation = "inputResourceVersion"

// UpdateInputsVersions generates a hash of the resource by combining its inputs.
func UpdateInputsVersions[T resource.Resource](out resource.Resource, inputs ...T) bool {
	return UpdateInputsAnnotation(out, xslices.Map(inputs, func(input T) string {
		return fmt.Sprintf("%s/%s@%s", input.Metadata().Type(), input.Metadata().ID(), input.Metadata().Version())
	})...)
}

// UpdateInputsAnnotation updates the annotation with the input resource version and returns if it has changed.
func UpdateInputsAnnotation(out resource.Resource, versions ...string) bool {
	hash := sha256.New()

	for i, version := range versions {
		if i > 0 {
			hash.Write([]byte(","))
		}

		hash.Write([]byte(version))
	}

	inVersion := hex.EncodeToString(hash.Sum(nil))

	version, found := out.Metadata().Annotations().Get(InputResourceVersionAnnotation)

	if found && version == inVersion {
		return false
	}

	out.Metadata().Annotations().Set(InputResourceVersionAnnotation, inVersion)

	return true
}

// CopyAllLabels copies all labels from one resource to another.
func CopyAllLabels(src, dst resource.Resource) {
	dst.Metadata().Labels().Do(func(tmp kvutils.TempKV) {
		for key, value := range src.Metadata().Labels().Raw() {
			tmp.Set(key, value)
		}
	})
}

// CopyLabels copies the labels from one resource to another.
func CopyLabels(src, dst resource.Resource, keys ...string) {
	dst.Metadata().Labels().Do(func(tmp kvutils.TempKV) {
		for _, key := range keys {
			if label, ok := src.Metadata().Labels().Get(key); ok {
				tmp.Set(key, label)
			}
		}
	})
}

// CopyAllAnnotations copies all annotations from one resource to another.
func CopyAllAnnotations(src, dst resource.Resource) {
	dst.Metadata().Annotations().Do(func(tmp kvutils.TempKV) {
		for key, value := range src.Metadata().Annotations().Raw() {
			tmp.Set(key, value)
		}
	})
}

// CopyAnnotations copies annotations from one resource to another.
func CopyAnnotations(src, dst resource.Resource, annotations ...string) {
	dst.Metadata().Annotations().Do(func(tmp kvutils.TempKV) {
		for _, key := range annotations {
			if label, ok := src.Metadata().Annotations().Get(key); ok {
				tmp.Set(key, label)
			}
		}
	})
}

// CopyUserLabels copies all user labels from one resource to another.
// It removes all user labels on the target that are not present in the source resource.
// System labels are not copied.
func CopyUserLabels(target resource.Resource, labels map[string]string) {
	ClearUserLabels(target)

	if len(labels) == 0 {
		return
	}

	target.Metadata().Labels().Do(func(tmp kvutils.TempKV) {
		for key, value := range labels {
			if strings.HasPrefix(key, omni.SystemLabelPrefix) {
				continue
			}

			tmp.Set(key, value)
		}
	})
}

// ClearUserLabels removes all user labels from the resource.
func ClearUserLabels(res resource.Resource) {
	res.Metadata().Labels().Do(func(tmp kvutils.TempKV) {
		for key := range res.Metadata().Labels().Raw() {
			if strings.HasPrefix(key, omni.SystemLabelPrefix) {
				continue
			}

			tmp.Delete(key)
		}
	})
}

// HandleInputOptions optional args for HandleInput.
type HandleInputOptions struct {
	id string
}

// HandleInputOption optional arg for HandleInput.
type HandleInputOption func(*HandleInputOptions)

// WithID maps the resource using another id.
func WithID(id string) HandleInputOption {
	return func(hio *HandleInputOptions) {
		hio.id = id
	}
}

// HandleInput reads the additional input resource and automatically manages finalizers.
// By default maps the resource using same id.
func HandleInput[T generic.ResourceWithRD, S generic.ResourceWithRD](ctx context.Context, r controller.ReaderWriter, finalizer string, main S, opts ...HandleInputOption) (T, error) {
	var zero T

	options := HandleInputOptions{
		id: main.Metadata().ID(),
	}

	for _, o := range opts {
		o(&options)
	}

	res, err := safe.ReaderGetByID[T](ctx, r, options.id)
	if err != nil {
		if state.IsNotFoundError(err) {
			return zero, nil
		}

		return zero, err
	}

	if res.Metadata().Phase() == resource.PhaseTearingDown || main.Metadata().Phase() == resource.PhaseTearingDown {
		if err := r.RemoveFinalizer(ctx, res.Metadata(), finalizer); err != nil && !state.IsNotFoundError(err) {
			return zero, err
		}

		if res.Metadata().Phase() == resource.PhaseTearingDown {
			return zero, nil
		}

		return res, nil
	}

	if !res.Metadata().Finalizers().Has(finalizer) {
		if err := r.AddFinalizer(ctx, res.Metadata(), finalizer); err != nil {
			return zero, err
		}
	}

	return res, nil
}

// GetTalosClient for the machine id.
// Automatically pick secure or insecure client.
func GetTalosClient[T interface {
	*V
	generic.ResourceWithRD
}, V any](ctx context.Context, r controller.Reader, address string, machineResource T) (*client.Client, error) {
	opts := talos.GetSocketOptions(address)

	createInsecureClient := func() (*client.Client, error) {
		return client.New(ctx,
			append(
				opts,
				client.WithTLSConfig(&tls.Config{
					InsecureSkipVerify: true,
				}),
				client.WithEndpoints(address),
			)...)
	}

	if machineResource == nil {
		return createInsecureClient()
	}

	machineStatusSnapshot, err := safe.ReaderGetByID[*omni.MachineStatusSnapshot](ctx, r, machineResource.Metadata().ID())
	if err != nil && !state.IsNotFoundError(err) {
		return nil, err
	}

	if machineStatusSnapshot != nil && machineStatusSnapshot.TypedSpec().Value.MachineStatus.Stage == machine.MachineStatusEvent_MAINTENANCE {
		return createInsecureClient()
	}

	clusterName, ok := machineResource.Metadata().Labels().Get(omni.LabelCluster)
	if !ok {
		return createInsecureClient()
	}

	talosConfig, err := safe.ReaderGet[*omni.TalosConfig](ctx, r, omni.NewTalosConfig(resources.DefaultNamespace, clusterName).Metadata())
	if err != nil && !state.IsNotFoundError(err) {
		return nil, fmt.Errorf("cluster '%s' failed to get talosconfig: %w", clusterName, err)
	}

	if talosConfig == nil {
		return createInsecureClient()
	}

	var endpoints []string

	if opts == nil {
		endpoints = []string{address}
	}

	config := omni.NewTalosClientConfig(talosConfig, endpoints...)
	opts = append(opts, client.WithConfig(config))

	result, err := client.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create client to machine '%s': %w", machineResource.Metadata().ID(), err)
	}

	return result, nil
}

// SetPatchesCompress compresses the patches and sets them in the ClusterMachineConfigPatches.
func SetPatchesCompress(res *omni.ClusterMachineConfigPatches, patches []*omni.ConfigPatch) error {
	for _, patch := range patches {
		compr, err := getCompressed(patch)
		if err != nil {
			return err
		} else if compr == nil {
			continue
		}

		if len(compr) < 1024 { // this is a small patch, decompress to check if it's all whitespace
			if IsEmptyPatch(patch) {
				continue
			}
		}

		// append the patch
		res.TypedSpec().Value.CompressedPatches = append(res.TypedSpec().Value.GetCompressedPatches(), compr)
	}

	return nil
}

func getCompressed(patch *omni.ConfigPatch) ([]byte, error) {
	if IsEmptyPatch(patch) {
		return nil, nil
	}

	compressedData := patch.TypedSpec().Value.GetCompressedData()
	if len(compressedData) > 0 {
		return compressedData, nil
	}

	buffer, err := patch.TypedSpec().Value.GetUncompressedData()
	if err != nil {
		return nil, err
	}

	defer buffer.Free()

	if err = patch.TypedSpec().Value.SetUncompressedData(buffer.Data()); err != nil {
		return nil, err
	}

	return patch.TypedSpec().Value.CompressedData, nil
}

// IsEmptyPatch checks if the patch is empty.
func IsEmptyPatch(patch *omni.ConfigPatch) bool {
	buffer, err := patch.TypedSpec().Value.GetUncompressedData()
	if err != nil {
		return false
	}

	defer buffer.Free()

	return len(bytes.TrimSpace(buffer.Data())) == 0
}
