// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Package client provides Omni API client.
package client

import (
	"github.com/siderolabs/go-api-signature/pkg/client/interceptor"
	"github.com/siderolabs/go-api-signature/pkg/pgp/client"
	"google.golang.org/grpc"

	"github.com/siderolabs/omni/client/pkg/client/omni"
	"github.com/siderolabs/omni/client/pkg/version"
)

// Options is the options for the client.
type Options struct {
	AuthInterceptor *interceptor.Interceptor

	AdditionalGRPCDialOptions []grpc.DialOption
	OmniClientOptions         []omni.Option

	InsecureSkipTLSVerify bool
}

// Option is a functional option for the client.
type Option func(*Options)

// WithInsecureSkipTLSVerify creates the client with insecure TLS verification.
func WithInsecureSkipTLSVerify(insecureSkipTLSVerify bool) Option {
	return func(options *Options) {
		options.InsecureSkipTLSVerify = insecureSkipTLSVerify
	}
}

// WithServiceAccount creates the client authenticating with the given service account.
func WithServiceAccount(serviceAccountBase64 string) Option {
	return func(options *Options) {
		options.AuthInterceptor = signatureAuthInterceptor("", "", serviceAccountBase64)
	}
}

// WithUserAccount is used for accessing Omni by a human.
func WithUserAccount(contextName, identity string) Option {
	return func(options *Options) {
		options.AuthInterceptor = signatureAuthInterceptor(contextName, identity, "")
	}
}

func signatureAuthInterceptor(contextName, identity, serviceAccountBase64 string) *interceptor.Interceptor {
	return interceptor.New(interceptor.Options{
		UserKeyProvider:      client.NewKeyProvider("omni/keys"),
		ContextName:          contextName,
		Identity:             identity,
		ClientName:           version.Name + " " + version.Tag,
		ServiceAccountBase64: serviceAccountBase64,
	})
}

// WithGrpcOpts adds additional gRPC dial options to the client.
func WithGrpcOpts(opts ...grpc.DialOption) Option {
	return func(options *Options) {
		options.AdditionalGRPCDialOptions = append(options.AdditionalGRPCDialOptions, opts...)
	}
}

// WithOmniClientOptions adds Omni client options.
func WithOmniClientOptions(opts ...omni.Option) Option {
	return func(options *Options) {
		options.OmniClientOptions = opts
	}
}
