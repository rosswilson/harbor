package platform

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type API struct {
	Controller *Controller
	Token      string
	Docker     *Docker
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 32768))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.Token)) != 1 {
		problem(w, 401, "API token required")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host {
			problem(w, 403, "Cross-origin requests are not allowed")
			return
		}
	}
	p := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"), "/"), "/")
	if r.Method == "GET" && p[0] == "state" {
		state := a.Controller.Store.copy()
		gs, v := a.Controller.XDS.Status()
		writeJSON(w, 200, map[string]any{"state": state, "gateways": gs, "xdsVersion": v})
		return
	}
	if r.Method == "GET" && p[0] == "rollouts" && len(p) == 2 {
		state := a.Controller.Store.copy()
		for _, ro := range state.Rollouts {
			if ro.ID == p[1] {
				writeJSON(w, 200, ro)
				return
			}
		}
		problem(w, 404, "Rollout not found")
		return
	}
	if len(p) == 3 && p[0] == "gateways" && r.Method == "POST" {
		a.gateway(w, r, p[1], p[2])
		return
	}
	s := a.Controller.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	var result any
	var err error
	status := 201
	switch {
	case r.Method == "POST" && len(p) == 1 && p[0] == "cells":
		var c Cell
		err = decode(r, &c)
		if err == nil {
			if !idPattern.MatchString(c.ID) || c.Region == "" {
				err = fmt.Errorf("valid cell id and region required")
			} else if s.State.cell(c.ID) != nil {
				err = fmt.Errorf("cell already exists")
			} else {
				s.State.Cells = append(s.State.Cells, c)
				result = c
				s.event("cell", c.ID, "Cell registered (logical local isolation)")
			}
		}
	case r.Method == "POST" && len(p) == 1 && p[0] == "revisions":
		var rev Revision
		err = decode(r, &rev)
		if err == nil {
			if rev.HealthPath == "" {
				rev.HealthPath = "/"
			}
			if !idPattern.MatchString(rev.ID) || rev.Image == "" || len(rev.Image) > 250 || strings.ContainsAny(rev.Image, " \n\r") || rev.Port < 1 || rev.Port > 65535 || !strings.HasPrefix(rev.HealthPath, "/") {
				err = fmt.Errorf("valid id, image, port and healthPath required")
			} else if s.State.revision(rev.ID) != nil {
				err = fmt.Errorf("revision already exists; revisions are immutable")
			} else {
				s.State.Revisions = append(s.State.Revisions, rev)
				result = rev
				s.event("revision", rev.ID, "Registered "+rev.Image)
			}
		}
	case r.Method == "POST" && len(p) == 1 && p[0] == "deployments":
		var input struct {
			ID       string `json:"id"`
			Cell     string `json:"cell"`
			Revision string `json:"revision"`
			Replicas int    `json:"replicas"`
		}
		err = decode(r, &input)
		if err == nil {
			if !idPattern.MatchString(input.ID) || s.State.cell(input.Cell) == nil || s.State.revision(input.Revision) == nil || input.Replicas < 1 || input.Replicas > 5 {
				err = fmt.Errorf("valid id, existing cell/revision and 1–5 replicas required")
			} else if s.State.deployment(input.ID) != nil {
				err = fmt.Errorf("deployment already exists")
			} else {
				d := Deployment{ID: input.ID, Cell: input.Cell, Revision: input.Revision, Replicas: input.Replicas, Status: "pending", Instances: []Instance{}}
				s.State.Deployments = append(s.State.Deployments, d)
				result = d
				s.event("deployment", d.ID, "Deployment requested")
			}
		}
	case r.Method == "DELETE" && len(p) == 2 && p[0] == "deployments":
		d := s.State.deployment(p[1])
		if d == nil {
			err = fmt.Errorf("deployment not found")
		} else {
			for _, t := range s.State.Tenants {
				if t.Deployment == d.ID || t.Previous == d.ID {
					err = fmt.Errorf("deployment is serving traffic or retained for tenant rollback")
				}
			}
			for _, ro := range s.State.Rollouts {
				if active(ro.Status) && (ro.Target == d.ID || ro.Previous == d.ID) {
					err = fmt.Errorf("deployment is referenced by an active rollout")
				}
			}
			if err == nil {
				d.Retired = true
				d.Status = "retiring"
				result = d
				s.event("deployment", d.ID, "Retirement requested")
			}
		}
	case (r.Method == "POST" && len(p) == 1 && p[0] == "tenants") || (r.Method == "PATCH" && len(p) == 2 && p[0] == "tenants"):
		var in struct {
			ID         string   `json:"id"`
			Name       string   `json:"name"`
			Cell       string   `json:"cell"`
			Deployment string   `json:"deployment"`
			Domains    []string `json:"domains"`
			Approval   bool     `json:"approval"`
		}
		err = decode(r, &in)
		if err == nil {
			if r.Method == "PATCH" {
				in.ID = p[1]
			}
			existing := s.State.tenant(in.ID)
			if r.Method == "PATCH" && existing != nil {
				in.Cell = existing.Cell
				in.Deployment = existing.Deployment
			}
			if !idPattern.MatchString(in.ID) || strings.TrimSpace(in.Name) == "" || len(in.Name) > 80 {
				err = fmt.Errorf("valid tenant id and name required")
			} else if r.Method == "POST" && existing != nil {
				err = fmt.Errorf("tenant already exists")
			} else if r.Method == "PATCH" && existing == nil {
				err = fmt.Errorf("tenant not found")
			} else if err = validateDomains(s.State, in.ID, in.Domains); err == nil {
				d := s.State.deployment(in.Deployment)
				cell := s.State.cell(in.Cell)
				if d == nil || cell == nil || d.Cell != in.Cell || d.Retired {
					err = fmt.Errorf("deployment must belong to the tenant cell")
				} else {
					if cell.Dedicated {
						for _, t := range s.State.Tenants {
							if t.Cell == cell.ID && t.ID != in.ID {
								err = fmt.Errorf("dedicated cell already has a tenant")
							}
						}
					}
					if err == nil {
						t := Tenant{ID: in.ID, Name: in.Name, Cell: in.Cell, Domains: in.Domains, Deployment: in.Deployment, Approval: in.Approval}
						if existing != nil {
							t.Previous = existing.Previous
							*existing = t
						} else {
							s.State.Tenants = append(s.State.Tenants, t)
						}
						result = t
						s.event("tenant", t.ID, "Tenant routing and policy saved")
					}
				}
			}
		}
	case r.Method == "POST" && len(p) == 1 && p[0] == "rollouts":
		var in struct {
			Tenant         string `json:"tenant"`
			Deployment     string `json:"deployment"`
			ObserveSeconds int    `json:"observeSeconds"`
		}
		err = decode(r, &in)
		if err == nil {
			result, err = a.newRollout(in.Tenant, in.Deployment, in.ObserveSeconds, r.Header.Get("Idempotency-Key"), false)
		}
	case r.Method == "POST" && len(p) == 3 && p[0] == "tenants" && p[2] == "rollback":
		t := s.State.tenant(p[1])
		if t == nil || t.Previous == "" {
			err = fmt.Errorf("no previous assignment available")
		} else {
			result, err = a.newRollout(t.ID, t.Previous, 10, r.Header.Get("Idempotency-Key"), true)
		}
	case r.Method == "POST" && len(p) == 3 && p[0] == "rollouts":
		ro := a.Controller.findRollout(p[1])
		if ro == nil {
			err = fmt.Errorf("rollout not found")
		} else if p[2] == "approve" && ro.Status == "awaiting_approval" {
			ro.Status = "preparing"
			ro.Created = time.Now().UTC()
			ro.Message = "Operator approved release"
			result = ro
			s.event("approval", ro.Tenant, ro.Message)
		} else if p[2] == "cancel" && active(ro.Status) && ro.Status != "reverting" {
			if ro.Status == "observing" || ro.Status == "activating" {
				s.State.tenant(ro.Tenant).Deployment = ro.Previous
				ro.Status = "reverting"
				ro.Message = "Operator requested rollback"
			} else {
				ro.Status = "cancelled"
				ro.Message = "Cancelled before activation"
			}
			result = ro
			s.event("release", ro.Tenant, ro.Message)
		} else {
			err = fmt.Errorf("action is not valid in the current rollout state")
		}
	default:
		problem(w, 404, "Unknown API route")
		return
	}
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	s.State.Generation++
	s.mustSave()
	writeJSON(w, status, result)
}
func validateDomains(s State, id string, domains []string) error {
	if len(domains) == 0 || len(domains) > 20 {
		return fmt.Errorf("provide 1–20 exact hostnames")
	}
	seen := map[string]bool{}
	for _, d := range domains {
		if len(d) > 253 || !domainPattern.MatchString(d) || strings.HasSuffix(d, ".preview.localhost") || seen[d] {
			return fmt.Errorf("invalid, duplicate or reserved hostname: %s", d)
		}
		seen[d] = true
		for _, t := range s.Tenants {
			if t.ID != id {
				for _, other := range t.Domains {
					if d == other {
						return fmt.Errorf("hostname already belongs to %s", t.ID)
					}
				}
			}
		}
	}
	return nil
}
func (a *API) newRollout(tenant, target string, seconds int, key string, recovery bool) (*Rollout, error) {
	s := a.Controller.Store
	if seconds == 0 {
		seconds = 20
	}
	if seconds < 6 || seconds > 600 {
		return nil, fmt.Errorf("observation window must be 6–600 seconds")
	}
	if key != "" {
		if len(key) > 128 {
			return nil, fmt.Errorf("idempotency key is too long")
		}
		for i := range s.State.Rollouts {
			ro := &s.State.Rollouts[i]
			if ro.Key == key {
				if ro.Tenant != tenant || ro.Target != target || ro.ObserveSeconds != seconds {
					return nil, fmt.Errorf("idempotency key already used for a different request")
				}
				return ro, nil
			}
		}
	}
	t := s.State.tenant(tenant)
	d := s.State.deployment(target)
	if t == nil || d == nil || d.Retired {
		return nil, fmt.Errorf("tenant and active deployment required")
	}
	if t.Cell != d.Cell {
		return nil, fmt.Errorf("release cannot move tenant placement; choose a deployment in %s", t.Cell)
	}
	if t.Deployment == d.ID {
		return nil, fmt.Errorf("tenant already uses this deployment")
	}
	for _, ro := range s.State.Rollouts {
		if ro.Tenant == tenant && active(ro.Status) {
			return nil, fmt.Errorf("tenant already has an active rollout")
		}
	}
	b := make([]byte, 6)
	rand.Read(b)
	status := "preparing"
	if t.Approval && !recovery {
		status = "awaiting_approval"
	}
	ro := Rollout{ID: "rel-" + hex.EncodeToString(b), Tenant: tenant, Target: target, Previous: t.Deployment, Status: status, Created: time.Now().UTC(), ObserveSeconds: seconds, Message: "Release requested", Key: key}
	s.State.Rollouts = append(s.State.Rollouts, ro)
	s.event("release", tenant, "Requested "+target)
	return &s.State.Rollouts[len(s.State.Rollouts)-1], nil
}
func (a *API) gateway(w http.ResponseWriter, r *http.Request, id, action string) {
	if (id != "gateway-a" && id != "gateway-b") || (action != "stop" && action != "start") {
		problem(w, 400, "Choose gateway-a/gateway-b and start/stop")
		return
	}
	name := "harbor-" + id
	var info containerInfo
	if err := a.Docker.call(r.Context(), "GET", "/containers/"+name+"/json", nil, &info); err != nil {
		problem(w, 502, err.Error())
		return
	}
	if info.Config.Labels["harbor.owner"] != "harbor-lab" {
		problem(w, 403, "Gateway ownership mismatch")
		return
	}
	path := "/containers/" + name + "/" + action
	if action == "stop" {
		path += "?t=5"
	}
	if err := a.Docker.call(r.Context(), "POST", path, nil, nil); err != nil {
		problem(w, 502, err.Error())
		return
	}
	s := a.Controller.Store
	s.mu.Lock()
	s.event("gateway", id, "Operator requested "+action)
	s.mustSave()
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"status": action + " requested"})
}
