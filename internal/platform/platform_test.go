package platform

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

func testAPI(t *testing.T) (*API, *Store) {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &API{Controller: &Controller{Store: s, XDS: NewXDS()}, Token: "test"}, s
}
func callAPI(a *API, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/"+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	return w
}
func TestHostnameOwnershipAndInputBoundaries(t *testing.T) {
	a, s := testAPI(t)
	for _, body := range []string{
		`{"id":"other","name":"Other","cell":"shared-eu","deployment":"shared-r41","domains":["betaco.localhost"]}`,
		`{"id":"other","name":"Other","cell":"shared-eu","deployment":"shared-r41","domains":["*.example.com"]}`,
		`{"id":"other","name":"Other","cell":"shared-eu","deployment":"shared-r41","domains":["shared-r41.preview.localhost"]}`,
		`{"id":"other","name":"Other","cell":"acme-eu","deployment":"acme-r41","domains":["other.localhost"]}`,
	} {
		if w := callAPI(a, "POST", "tenants", body, ""); w.Code != 400 {
			t.Fatalf("accepted invalid tenant: %s", w.Body)
		}
	}
	if len(s.State.Tenants) != 3 {
		t.Fatal("failed requests mutated tenants")
	}
}
func TestReleaseApprovalIdempotencyAndIsolation(t *testing.T) {
	a, s := testAPI(t)
	s.State.Deployments = append(s.State.Deployments, Deployment{ID: "acme-r42", Cell: "acme-eu", Revision: "r42", Replicas: 1})
	payload := `{"tenant":"acme","deployment":"acme-r42","observeSeconds":10}`
	first := callAPI(a, "POST", "rollouts", payload, "build-1")
	if first.Code != 201 {
		t.Fatal(first.Body)
	}
	var r Rollout
	json.Unmarshal(first.Body.Bytes(), &r)
	if r.Status != "awaiting_approval" || s.State.tenant("acme").Deployment != "acme-r41" {
		t.Fatal("approval was bypassed")
	}
	repeat := callAPI(a, "POST", "rollouts", payload, "build-1")
	var again Rollout
	json.Unmarshal(repeat.Body.Bytes(), &again)
	if again.ID != r.ID || len(s.State.Rollouts) != 1 {
		t.Fatal("idempotency failed")
	}
	if w := callAPI(a, "POST", "rollouts", `{"tenant":"betaco","deployment":"shared-r42","observeSeconds":10}`, "build-1"); w.Code != 400 {
		t.Fatal("key conflict accepted")
	}
	if w := callAPI(a, "POST", "rollouts", `{"tenant":"betaco","deployment":"acme-r42"}`, ""); w.Code != 400 {
		t.Fatal("cross-cell release accepted")
	}
	if w := callAPI(a, "POST", "rollouts/"+r.ID+"/approve", "{}", ""); w.Code != 201 {
		t.Fatal(w.Body)
	}
	if s.State.Rollouts[0].Status != "preparing" {
		t.Fatal("approval not applied")
	}
	reloaded, err := NewStore(s.path)
	if err != nil || reloaded.State.Rollouts[0].Status != "preparing" {
		t.Fatal("approved operation not persisted", err)
	}
}
func TestSnapshotRoutesAndRetirementGuard(t *testing.T) {
	a, s := testAPI(t)
	s.State.tenant("betaco").Deployment = "shared-r42"
	snapshot, err := buildSnapshot(s.State, "test")
	if err != nil {
		t.Fatal(err)
	}
	resources := snapshot.GetResources(resource.RouteType)
	config := resources["tenant-routes"].(*route.RouteConfiguration)
	for _, v := range config.VirtualHosts {
		if v.Name == "betaco" {
			if len(v.Domains) != 2 || v.Routes[0].GetRoute().GetCluster() != "shared-r42" {
				t.Fatal("wrong BetaCo assignment")
			}
		}
		if v.Name == "smallco" && v.Routes[0].GetRoute().GetCluster() != "shared-r41" {
			t.Fatal("release affected another tenant")
		}
	}
	if w := callAPI(a, "DELETE", "deployments/shared-r41", "", ""); w.Code != 400 {
		t.Fatal("retired an active deployment")
	}
}
func TestAuthAndCrossOrigin(t *testing.T) {
	a, _ := testAPI(t)
	for _, tc := range []struct {
		token, origin string
		want          int
	}{{"", "", 401}, {"test", "https://untrusted.example", 403}} {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(w.Code)
		}
	}
}
