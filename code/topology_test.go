package main

import (
	"testing"
)

// fakeStatus returns a status lookup func backed by a map of connected names.
func fakeStatus(running map[string]bool) func(string) ForwardStatus {
	return func(name string) ForwardStatus {
		if running[name] {
			return ForwardStatus{Running: true, PublicURL: "https://example.l.tunwg.com"}
		}
		return ForwardStatus{}
	}
}

func findNode(t *testing.T, topo Topology, id string) TopoNode {
	t.Helper()
	for _, n := range topo.Nodes {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not found in %+v", id, topo.Nodes)
	return TopoNode{}
}

func TestBuildTopologyEmpty(t *testing.T) {
	topo := buildTopology("l.tunwg.com", nil, fakeStatus(nil))
	if len(topo.Nodes) != 1 || len(topo.Edges) != 0 {
		t.Fatalf("expected root-only graph, got %+v", topo)
	}
	root := topo.Nodes[0]
	if root.ID != "root" || root.ConnType != "wireguard" || !root.Online {
		t.Errorf("unexpected root anchor: %+v", root)
	}
	// Edges must be an empty slice (encodes as [], not null).
	if topo.Edges == nil {
		t.Error("Edges must be non-nil")
	}
}

func TestBuildTopologyForwards(t *testing.T) {
	forwards := []Forward{
		{Name: "zzz-cam", LocalURL: "http://10.0.0.7", Enabled: true},
		{Name: "ha", LocalURL: "http://192.168.2.50:8123", Enabled: true},
		{Name: "paused", LocalURL: "http://192.168.2.60:80", Enabled: false},
	}
	topo := buildTopology("l.tunwg.com", forwards, fakeStatus(map[string]bool{"ha": true}))

	// root + 3 services + relay
	if len(topo.Nodes) != 5 {
		t.Fatalf("expected 5 nodes, got %+v", topo.Nodes)
	}
	if len(topo.Edges) != 4 {
		t.Fatalf("expected 4 edges, got %+v", topo.Edges)
	}

	if topo.Nodes[0].ID != "root" {
		t.Errorf("first node must be the root anchor, got %+v", topo.Nodes[0])
	}

	ha := findNode(t, topo, "forward:ha")
	if ha.Kind != "service" || ha.Name != "ha" || ha.IP != "192.168.2.50:8123" || !ha.Online {
		t.Errorf("unexpected ha node: %+v", ha)
	}

	cam := findNode(t, topo, "forward:zzz-cam")
	if cam.Online {
		t.Errorf("zzz-cam is not running, expected offline: %+v", cam)
	}
	if cam.IP != "10.0.0.7" {
		t.Errorf("expected bare host for portless URL, got %q", cam.IP)
	}

	relay := findNode(t, topo, "relay")
	if relay.Kind != "relay" || relay.Name != "l.tunwg.com" || !relay.Online {
		t.Errorf("unexpected relay node: %+v", relay)
	}

	// Service nodes are sorted by name between root and relay.
	if topo.Nodes[1].Name != "ha" || topo.Nodes[2].Name != "paused" || topo.Nodes[3].Name != "zzz-cam" {
		t.Errorf("service nodes not sorted: %+v", topo.Nodes)
	}

	// Edges: each service -> root over "lan", then root -> relay over "tunnel".
	for i, svc := range []string{"forward:ha", "forward:paused", "forward:zzz-cam"} {
		e := topo.Edges[i]
		if e.From != svc || e.To != "root" || e.Layer != "lan" || e.Kind != "http" {
			t.Errorf("unexpected service edge: %+v", e)
		}
	}
	last := topo.Edges[len(topo.Edges)-1]
	if last.From != "root" || last.To != "relay" || last.Layer != "tunnel" || last.Kind != "wireguard" {
		t.Errorf("unexpected relay edge: %+v", last)
	}
}

func TestBuildTopologyAllDownRelayOffline(t *testing.T) {
	forwards := []Forward{
		{Name: "ha", LocalURL: "http://192.168.2.50:8123", Enabled: true},
	}
	topo := buildTopology("tunnel.example.org", forwards, fakeStatus(nil))

	relay := findNode(t, topo, "relay")
	if relay.Online {
		t.Errorf("no forward is up, relay must be offline: %+v", relay)
	}
	if relay.Name != "tunnel.example.org" {
		t.Errorf("relay must be named after the configured domain: %+v", relay)
	}
	if findNode(t, topo, "forward:ha").Online {
		t.Error("ha must be offline")
	}
}

func TestForwardHost(t *testing.T) {
	cases := map[string]string{
		"http://192.168.2.50:8123": "192.168.2.50:8123",
		"http://10.0.0.7":          "10.0.0.7",
		"https://[fd00::1]:8443":   "[fd00::1]:8443",
		"://not a url":             "",
	}
	for raw, want := range cases {
		if got := forwardHost(raw); got != want {
			t.Errorf("forwardHost(%q) = %q, want %q", raw, got, want)
		}
	}
}
