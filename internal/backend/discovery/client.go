// Copyright (c) 2025 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

package discovery

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	serverpb "github.com/siderolabs/discovery-api/api/v1alpha1/server/pb"
	discoveryclient "github.com/siderolabs/discovery-client/pkg/client"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/siderolabs/omni/internal/pkg/siderolink"
)

const (
	callTimeout = 5 * time.Second
	defaultTTL  = 30 * time.Minute
)

// Client is a client for the discovery service.
type Client struct {
	conn          *grpc.ClientConn
	clusterClient serverpb.ClusterClient
}

// Options are the options for the discovery service client.
type Options struct {
	UseEmbeddedDiscoveryService  bool
	EmbeddedDiscoveryServicePort int
}

// NewClient creates a new discovery service client.
func NewClient(options Options) (*Client, error) {
	conn, err := createConn(options)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection to discovery service: %w", err)
	}

	return &Client{
		conn:          conn,
		clusterClient: serverpb.NewClusterClient(conn),
	}, nil
}

// AffiliateDelete deletes the given affiliate from the given cluster.
func (client *Client) AffiliateDelete(ctx context.Context, cluster, affiliate string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	if _, err := client.clusterClient.AffiliateDelete(ctx, &serverpb.AffiliateDeleteRequest{
		ClusterId:   cluster,
		AffiliateId: affiliate,
	}); err != nil {
		return fmt.Errorf("failed to delete affiliate %q for cluster %q: %w", affiliate, cluster, err)
	}

	return nil
}

// Close closes the underlying connection to the discovery service.
func (client *Client) Close() error {
	return client.conn.Close()
}

// createConn creates a gRPC connection to the discovery service.
func createConn(options Options) (*grpc.ClientConn, error) {
	var (
		transportCredentials credentials.TransportCredentials
		target               string
	)

	if options.UseEmbeddedDiscoveryService {
		target = net.JoinHostPort(siderolink.ListenHost, strconv.Itoa(options.EmbeddedDiscoveryServicePort))
		transportCredentials = insecure.NewCredentials()
	} else {
		u, err := url.Parse(constants.DefaultDiscoveryServiceEndpoint)
		if err != nil {
			return nil, err
		}

		target = net.JoinHostPort(u.Host, "443")
		transportCredentials = credentials.NewTLS(&tls.Config{})
	}

	opts := discoveryclient.GRPCDialOptions(discoveryclient.Options{
		TTL: defaultTTL,
	})

	opts = append(opts, grpc.WithSharedWriteBuffer(true), grpc.WithTransportCredentials(transportCredentials))

	discoveryConn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, err
	}

	return discoveryConn, nil
}
