package platform

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	types "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

type XDS struct {
	mu       sync.Mutex
	cache    cache.SnapshotCache
	gateways map[string]*Gateway
	streams  map[int64]string
	version  string
	epoch    string
}

func NewXDS() *XDS {
	x := &XDS{cache: cache.NewSnapshotCache(true, cache.IDHash{}, nil), gateways: map[string]*Gateway{}, streams: map[int64]string{}}
	// A fresh process must elicit fresh ACKs even when routing is unchanged.
	// Otherwise Envoy may report a matching cached version and receive no update.
	epoch := make([]byte, 4)
	if _, err := rand.Read(epoch); err != nil {
		panic(err)
	}
	x.epoch = fmt.Sprintf("%x", epoch)
	for _, id := range []string{"gateway-a", "gateway-b"} {
		x.gateways[id] = &Gateway{ID: id, ACK: map[string]string{}}
	}
	return x
}
func (x *XDS) Serve(ctx context.Context, address string) error {
	callbacks := server.CallbackFuncs{StreamRequestFunc: func(stream int64, req *discovery.DiscoveryRequest) error {
		x.mu.Lock()
		defer x.mu.Unlock()
		id := x.streams[stream]
		if req.Node != nil {
			id = req.Node.Id
			x.streams[stream] = id
		}
		g := x.gateways[id]
		if g == nil {
			return fmt.Errorf("unknown gateway")
		}
		g.Connected = true
		g.LastSeen = time.Now().UTC()
		if req.ResponseNonce != "" {
			if req.ErrorDetail != nil {
				g.NACK = req.ErrorDetail.Message
			} else {
				g.ACK[req.TypeUrl] = req.VersionInfo
				g.NACK = ""
			}
		}
		return nil
	}, StreamClosedFunc: func(stream int64, _ *core.Node) {
		x.mu.Lock()
		defer x.mu.Unlock()
		id := x.streams[stream]
		delete(x.streams, stream)
		if g := x.gateways[id]; g != nil {
			g.Connected = false
			for _, other := range x.streams {
				if other == id {
					g.Connected = true
				}
			}
		}
	}}
	srv := grpc.NewServer()
	discovery.RegisterAggregatedDiscoveryServiceServer(srv, server.NewServer(ctx, x.cache, callbacks))
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); srv.Stop() }()
	return srv.Serve(ln)
}
func ads() *core.ConfigSource {
	return &core.ConfigSource{ResourceApiVersion: core.ApiVersion_V3, ConfigSourceSpecifier: &core.ConfigSource_Ads{Ads: &core.AggregatedConfigSource{}}}
}
func socket(host string, port int) *core.Address {
	return &core.Address{Address: &core.Address_SocketAddress{SocketAddress: &core.SocketAddress{Address: host, PortSpecifier: &core.SocketAddress_PortValue{PortValue: uint32(port)}}}}
}
func header(key, value string) *core.HeaderValueOption {
	return &core.HeaderValueOption{Header: &core.HeaderValue{Key: key, Value: value}, AppendAction: core.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD}
}

func buildSnapshot(s State, version string) (*cache.Snapshot, error) {
	clusters := []types.Resource{}
	endpoints := []types.Resource{}
	vhosts := []*route.VirtualHost{}
	for _, d := range s.Deployments {
		if d.Retired {
			continue
		}
		rev := s.revision(d.Revision)
		if rev == nil {
			continue
		}
		clusters = append(clusters, &cluster.Cluster{Name: d.ID, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_EDS}, EdsClusterConfig: &cluster.Cluster_EdsClusterConfig{EdsConfig: ads()}, LbPolicy: cluster.Cluster_ROUND_ROBIN})
		eps := []*endpoint.LbEndpoint{}
		for _, in := range d.Instances {
			if in.Ready && in.IP != "" {
				eps = append(eps, &endpoint.LbEndpoint{HostIdentifier: &endpoint.LbEndpoint_Endpoint{Endpoint: &endpoint.Endpoint{Address: socket(in.IP, rev.Port)}}})
			}
		}
		endpoints = append(endpoints, &endpoint.ClusterLoadAssignment{ClusterName: d.ID, Endpoints: []*endpoint.LocalityLbEndpoints{{LbEndpoints: eps}}})
		vhosts = append(vhosts, virtualHost("preview-"+d.ID, []string{d.ID + ".preview.localhost"}, d.ID, "preview"))
	}
	for _, t := range s.Tenants {
		d := s.deployment(t.Deployment)
		if d != nil && !d.Retired {
			vhosts = append(vhosts, virtualHost(t.ID, t.Domains, d.ID, t.ID))
		}
	}
	vhosts = append(vhosts, &route.VirtualHost{Name: "unknown-host", Domains: []string{"*"}, Routes: []*route.Route{{Match: &route.RouteMatch{PathSpecifier: &route.RouteMatch_Prefix{Prefix: "/"}}, Action: &route.Route_DirectResponse{DirectResponse: &route.DirectResponseAction{Status: 404, Body: &core.DataSource{Specifier: &core.DataSource_InlineString{InlineString: "Harbor: no tenant owns this hostname\n"}}}}}}})
	// VirtualHost header values use OVERWRITE_IF_EXISTS_OR_ADD, so untrusted
	// client values are replaced. A route-config removal would run afterwards.
	routes := &route.RouteConfiguration{Name: "tenant-routes", VirtualHosts: vhosts, IgnorePortInHostMatching: true, ResponseHeadersToAdd: []*core.HeaderValueOption{header("x-harbor-gateway", "%HOSTNAME%")}}
	routerAny, _ := anypb.New(&router.Router{})
	managerAny, _ := anypb.New(&hcm.HttpConnectionManager{StatPrefix: "ingress", CodecType: hcm.HttpConnectionManager_AUTO, RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{ConfigSource: ads(), RouteConfigName: "tenant-routes"}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: routerAny}}}, StreamIdleTimeout: durationpb.New(60 * time.Second)})
	l := &listener.Listener{Name: "http", Address: socket("0.0.0.0", 8080), FilterChains: []*listener.FilterChain{{Filters: []*listener.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &listener.Filter_TypedConfig{TypedConfig: managerAny}}}}}}
	snap, err := cache.NewSnapshot(version, map[resource.Type][]types.Resource{resource.ClusterType: clusters, resource.EndpointType: endpoints, resource.RouteType: {routes}, resource.ListenerType: {l}})
	if err != nil {
		return nil, err
	}
	return snap, snap.Consistent()
}
func virtualHost(id string, domains []string, dep, tenant string) *route.VirtualHost {
	return &route.VirtualHost{Name: id, Domains: domains, Routes: []*route.Route{{Match: &route.RouteMatch{PathSpecifier: &route.RouteMatch_Prefix{Prefix: "/"}}, Action: &route.Route_Route{Route: &route.RouteAction{ClusterSpecifier: &route.RouteAction_Cluster{Cluster: dep}, Timeout: durationpb.New(15 * time.Second)}}}}, RequestHeadersToAdd: []*core.HeaderValueOption{header("x-harbor-tenant", tenant), header("x-harbor-deployment", dep)}, ResponseHeadersToAdd: []*core.HeaderValueOption{header("x-harbor-tenant", tenant), header("x-harbor-deployment", dep)}, IncludeRequestAttemptCount: true}
}

func (x *XDS) Publish(ctx context.Context, s State) error {
	// Hash only routing inputs; rollout events and probe counters must not churn xDS.
	b, _ := json.Marshal(struct {
		D []Deployment
		T []Tenant
		R []Revision
	}{s.Deployments, s.Tenants, s.Revisions})
	sum := sha256.Sum256(b)
	v := x.epoch + "-" + fmt.Sprintf("%x", sum[:8])
	x.mu.Lock()
	same := x.version == v
	x.mu.Unlock()
	if same {
		return nil
	}
	snap, err := buildSnapshot(s, v)
	if err != nil {
		return err
	}
	for _, id := range []string{"gateway-a", "gateway-b"} {
		if err = x.cache.SetSnapshot(ctx, id, snap); err != nil {
			return err
		}
	}
	x.mu.Lock()
	x.version = v
	x.mu.Unlock()
	return nil
}
func (x *XDS) Status() ([]Gateway, string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := []Gateway{}
	for _, id := range []string{"gateway-a", "gateway-b"} {
		b, _ := json.Marshal(x.gateways[id])
		var g Gateway
		json.Unmarshal(b, &g)
		out = append(out, g)
	}
	return out, x.version
}
func (x *XDS) converged() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, g := range x.gateways {
		if !g.Connected || !g.Ready || g.NACK != "" || g.ACK[resource.RouteType] != x.version || g.ACK[resource.EndpointType] != x.version {
			return false
		}
	}
	return true
}
func (x *XDS) check(ctx context.Context) {
	for _, id := range []string{"gateway-a", "gateway-b"} {
		ready := probe(ctx, "http://"+id+":9901/ready", "")
		x.mu.Lock()
		x.gateways[id].Ready = ready
		x.mu.Unlock()
	}
}
