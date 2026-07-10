package main

import (
	"net/http"
	"net/url"
	"sort"
)

// TopoNode / TopoEdge / Topology mirror the shapes the SPR host expects from
// plugin topology endpoints (see spr-tailscale). The host merges the plugin
// graph into the router topology at the "root" anchor node.
type TopoNode struct {
	ID       string
	Kind     string
	Name     string
	IP       string `json:",omitempty"`
	ConnType string `json:",omitempty"`
	Online   bool
}

type TopoEdge struct {
	From  string
	To    string
	Layer string
	Kind  string
}

type Topology struct {
	Nodes []TopoNode
	Edges []TopoEdge
}

// forwardHost extracts the display "host:port" (or bare host) from a
// forward's LocalURL. Returns "" if the URL does not parse.
func forwardHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// buildTopology builds the plugin's contribution to the SPR topology view:
//
//	service (per forward) --lan--> root --tunnel--> relay
//
// The root anchor represents this plugin (transport: userspace WireGuard).
// Each forward contributes one service node for its LAN target, Online when
// its tunwg child process is running. A single relay node represents the
// tunwg relay domain, Online when any forward's tunnel is up. With no
// forwards configured the graph is just the root anchor.
//
// status is injected so tests can use a fake data source.
func buildTopology(apiDomain string, forwards []Forward, status func(name string) ForwardStatus) Topology {
	topo := Topology{
		Nodes: []TopoNode{{ID: "root", ConnType: "wireguard", Online: true}},
		Edges: []TopoEdge{},
	}
	if len(forwards) == 0 {
		return topo
	}

	services := make([]TopoNode, 0, len(forwards))
	anyUp := false
	for _, f := range forwards {
		online := status(f.Name).Running
		if online {
			anyUp = true
		}
		services = append(services, TopoNode{
			ID:     "forward:" + f.Name,
			Kind:   "service",
			Name:   f.Name,
			IP:     forwardHost(f.LocalURL),
			Online: online,
		})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })

	for _, svc := range services {
		topo.Nodes = append(topo.Nodes, svc)
		topo.Edges = append(topo.Edges, TopoEdge{From: svc.ID, To: "root", Layer: "lan", Kind: "http"})
	}

	topo.Nodes = append(topo.Nodes, TopoNode{
		ID:     "relay",
		Kind:   "relay",
		Name:   apiDomain,
		Online: anyUp,
	})
	topo.Edges = append(topo.Edges, TopoEdge{From: "root", To: "relay", Layer: "tunnel", Kind: "wireguard"})

	return topo
}

func handleGetTopology(w http.ResponseWriter, r *http.Request) {
	configMtx.Lock()
	apiDomain := effectiveAPIDomain(gConfig)
	forwards := append([]Forward(nil), gConfig.Forwards...)
	configMtx.Unlock()

	writeJSON(w, buildTopology(apiDomain, forwards, gManager.Status))
}
