// Copyright (c) 2025 Sidero Labs, Inc.
//
// Use of this software is governed by the Business Source License
// included in the LICENSE file.

package workloadproxy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/siderolabs/gen/xiter"
	"github.com/siderolabs/go-loadbalancer/upstream"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Reconciler reconciles the load balancers for a cluster.
//
//nolint:govet
type Reconciler struct {
	logger    *zap.Logger
	logLevel  zapcore.Level
	transport *http.Transport

	mu               sync.Mutex
	clusterUpstreams map[resource.ID]*lazyUpstreams
	aliasToCluster   map[string]resource.ID
}

// NewReconciler creates a new Reconciler.
func NewReconciler(logger *zap.Logger, logLevel zapcore.Level) *Reconciler {
	if logger == nil {
		logger = zap.NewNop()
	}

	trans := cleanhttp.DefaultPooledTransport()

	rec := &Reconciler{
		logger:         logger,
		logLevel:       logLevel,
		transport:      trans,
		aliasToCluster: map[string]resource.ID{},
	}

	rec.setTransportDialer(trans)

	return rec
}

func (registry *Reconciler) init(cluster resource.ID) {
	if registry.clusterUpstreams == nil {
		registry.clusterUpstreams = map[resource.ID]*lazyUpstreams{}
	}

	if registry.aliasToCluster == nil {
		registry.aliasToCluster = map[string]resource.ID{}
	}

	if _, ok := registry.clusterUpstreams[cluster]; !ok {
		registry.clusterUpstreams[cluster] = &lazyUpstreams{
			clusterID: cluster,
			logger: func(msg string, fields ...zap.Field) {
				registry.logger.Log(registry.logLevel, msg, fields...)
			},
		}
	}
}

// Reconcile reconciles the workload proxy load balancers for a cluster.
func (registry *Reconciler) Reconcile(cluster resource.ID, aliasToUpstreamAddresses map[string][]string) error {
	registry.logger.Log(registry.logLevel, "reconcile LBs", zap.String("cluster", cluster))

	registry.mu.Lock()
	defer registry.mu.Unlock()

	reflect.New(reflect.TypeOf(cluster).Elem()).IsZero()

	registry.init(cluster)

	for als, cl := range registry.aliasToCluster {
		if cluster != cl {
			continue
		}

		if _, ok := aliasToUpstreamAddresses[als]; ok {
			registry.aliasToCluster[als] = cluster

			continue
		}

		delete(registry.aliasToCluster, als)
	}

	for als := range aliasToUpstreamAddresses {
		registry.aliasToCluster[als] = cluster
	}

	// drop removed LBs
	err := registry.clusterUpstreams[cluster].MergeState(aliasToUpstreamAddresses)
	if err != nil {
		return fmt.Errorf("failed to merge state: %w", err)
	}

	return nil
}

// GetProxy returns a proxy for the exposed service, targeting the load balancer for the given alias.
func (registry *Reconciler) GetProxy(als string) (http.Handler, resource.ID, error) {
	registry.mu.Lock()
	defer registry.mu.Unlock()

	clusterID, ok := registry.aliasToCluster[als]
	if !ok {
		return nil, "", nil
	}

	p := registry.clusterUpstreams[clusterID].PortForAlias(als)
	if p == "" {
		return nil, "", nil
	}

	proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: net.JoinHostPort(clusterID, p)})
	proxy.Transport = registry.transport
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		registry.logger.Error(
			"proxy error",
			zap.Error(err),
			zap.String("cluster", clusterID),
			zap.String("alias", als),
			zap.String("alias_host", req.Host),
		)

		http.Error(w, "workload proxy error", http.StatusBadGateway)
	}

	return proxy, clusterID, nil
}

func (registry *Reconciler) setTransportDialer(trans *http.Transport) {
	d := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	trans.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		clusterID, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy dst address: %w", err)
		}

		rem, err := registry.clusterUpstreams[clusterID].PickUpstream(port)

		switch {
		case err != nil:
			return nil, fmt.Errorf("failed to pick upstream for cluster %q: %w", clusterID, err)
		case rem == nil:
			return nil, fmt.Errorf("failed to pick upstream for cluster %q", clusterID)
		default:
			return d.DialContext(ctx, network, rem.Addr)
		}
	}
}

type lazyUpstreams struct { //nolint:govet
	clusterID string
	logger    func(string, ...zap.Field)

	mx            sync.Mutex
	upstreamAddrs []string          // addresses of the upstreams without port
	aliasToPorts  map[string]string // alias to port mapping
	l             *upstream.List[*remote]
	inUsePort     string
	t             *time.Timer
}

func (lu *lazyUpstreams) PortForAlias(alias string) string {
	if lu == nil {
		return ""
	}

	lu.mx.Lock()
	defer lu.mx.Unlock()

	return lu.aliasToPorts[alias]
}

func (lu *lazyUpstreams) PickUpstream(port string) (*remote, error) {
	if lu == nil {
		return nil, errors.New("remote doesnt exist")
	}

	lu.mx.Lock()
	defer lu.mx.Unlock()

	if _, ok := xiter.Find(func(v string) bool { return port == v }, maps.Values(lu.aliasToPorts)); !ok {
		return nil, fmt.Errorf("port %q is not in the list of ports", port)
	}

	if lu.t != nil {
		lu.t.Stop()
	}

	if lu.l == nil {
		it := xiter.Map(
			func(addr string) *remote { return &remote{Addr: net.JoinHostPort(addr, port)} },
			slices.Values(lu.upstreamAddrs),
		)

		l, err := upstream.NewListWithCmp(it, func(a, b *remote) bool { return a.Addr == b.Addr })
		if err != nil {
			return nil, err
		}

		lu.l = l
		lu.inUsePort = port
	}

	if lu.t == nil {
		lu.t = time.AfterFunc(5*time.Minute, lu.shutdown)
	} else {
		lu.t.Reset(5 * time.Minute)
	}

	return lu.l.Pick()
}

func (lu *lazyUpstreams) shutdown() {
	lu.mx.Lock()
	defer lu.mx.Unlock()

	if lu.l == nil {
		return
	}

	lu.l.Shutdown()

	lu.l = nil
}

func (lu *lazyUpstreams) MergeState(m map[string][]string) error {
	if lu == nil {
		return nil
	}

	lu.mx.Lock()
	defer lu.mx.Unlock()

	upstreamAddrs := takeRandom(m)

	// Delete the upstreams that are not in the new state
	lu.upstreamAddrs = slices.DeleteFunc(lu.upstreamAddrs, func(addr string) bool {
		if slices.Contains(upstreamAddrs, addr) {
			return false
		}

		lu.logger("remove LB", zap.String("cluster", lu.clusterID), zap.String("upstream", addr))

		return true
	})

	currentPort := lu.inUsePort

	// Delete the aliases that are not in the new state
	maps.DeleteFunc(lu.aliasToPorts, func(alias, port string) bool {
		if _, ok := m[alias]; ok {
			return false
		}

		if port == currentPort {
			currentPort = ""
		}

		lu.logger("remove LB", zap.String("cluster", lu.clusterID), zap.String("alias", alias))

		return true
	})

	// Add new upstreams
	for _, addr := range upstreamAddrs {
		if slices.Contains(lu.upstreamAddrs, addr) {
			continue
		}

		lu.logger("add LB", zap.String("cluster", lu.clusterID), zap.String("upstream", addr))

		lu.upstreamAddrs = append(lu.upstreamAddrs, addr)
	}

	// Add new aliases
	if lu.aliasToPorts == nil {
		lu.aliasToPorts = map[string]string{}
	}

	for als, upstreams := range m {
		if _, ok := lu.aliasToPorts[als]; ok || len(upstreams) == 0 {
			continue
		}

		ups := upstreams[0]

		_, p, err := net.SplitHostPort(ups)
		if err != nil {
			return fmt.Errorf("failed to split host and port '%q' for alus '%q': %w", ups, als, err)
		}

		lu.logger("add LB", zap.String("cluster", lu.clusterID), zap.String("upstream", ups))

		lu.aliasToPorts[als] = p
	}

	if lu.l == nil {
		return nil // no load balancer running, no need to update the upstreams in it
	}

	if len(lu.upstreamAddrs) == 0 || len(lu.aliasToPorts) == 0 {
		lu.logger("no upstreams or ports, shutting down LB", zap.String("cluster", lu.clusterID))

		lu.t.Stop()
		lu.l.Shutdown() // no upstreams or ports, no need to keep the load balancer running

		lu.l = nil
		lu.upstreamAddrs = nil
		lu.aliasToPorts = nil

		return nil
	}

	if currentPort == "" {
		// old port no longer valid, need to select a new one
		currentPort = takeRandom(lu.aliasToPorts)

		return nil
	}

	lu.logger(
		"reconcile LB",
		zap.String("cluster", lu.clusterID),
		zap.Strings("upstreams", lu.upstreamAddrs),
		zap.String("with_port", currentPort),
	)

	u := xiter.Map(
		func(addr string) *remote { return &remote{Addr: net.JoinHostPort(addr, currentPort)} },
		slices.Values(lu.upstreamAddrs),
	)

	lu.l.Reconcile(u)

	return nil
}

type remote struct {
	Addr string
}

func (r *remote) HealthCheck(context.Context) (upstream.Tier, error) { return 0, nil }

func takeRandom[K comparable, V any](m map[K]V) V {
	for _, v := range m {
		return v
	}

	return *new(V)
}
