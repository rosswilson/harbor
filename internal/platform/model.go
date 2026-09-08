package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Cell struct {
	ID        string `json:"id"`
	Region    string `json:"region"`
	Dedicated bool   `json:"dedicated"`
}
type Revision struct {
	ID         string            `json:"id"`
	Image      string            `json:"image"`
	Port       int               `json:"port"`
	HealthPath string            `json:"healthPath"`
	Env        map[string]string `json:"env"`
}
type Instance struct {
	Name  string `json:"name"`
	IP    string `json:"ip"`
	Ready bool   `json:"ready"`
}
type Deployment struct {
	ID        string     `json:"id"`
	Cell      string     `json:"cell"`
	Revision  string     `json:"revision"`
	Replicas  int        `json:"replicas"`
	Status    string     `json:"status"`
	Error     string     `json:"error"`
	Instances []Instance `json:"instances"`
	ImageID   string     `json:"imageId"`
	Retired   bool       `json:"retired"`
}
type Tenant struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Cell       string   `json:"cell"`
	Domains    []string `json:"domains"`
	Deployment string   `json:"deployment"`
	Previous   string   `json:"previous"`
	Approval   bool     `json:"approval"`
}
type Rollout struct {
	ID              string     `json:"id"`
	Tenant          string     `json:"tenant"`
	Target          string     `json:"target"`
	Previous        string     `json:"previous"`
	Status          string     `json:"status"`
	Created         time.Time  `json:"created"`
	Activated       *time.Time `json:"activated,omitempty"`
	ObserveSeconds  int        `json:"observeSeconds"`
	ObservedSeconds int        `json:"observedSeconds"`
	Samples         int        `json:"samples"`
	Failures        int        `json:"failures"`
	Consecutive     int        `json:"consecutive"`
	Message         string     `json:"message"`
	Key             string     `json:"key,omitempty"`
}
type Event struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject"`
	Message string    `json:"message"`
}
type State struct {
	Cells       []Cell       `json:"cells"`
	Revisions   []Revision   `json:"revisions"`
	Deployments []Deployment `json:"deployments"`
	Tenants     []Tenant     `json:"tenants"`
	Rollouts    []Rollout    `json:"rollouts"`
	Events      []Event      `json:"events"`
	Generation  int64        `json:"generation"`
}
type Gateway struct {
	ID        string            `json:"id"`
	Connected bool              `json:"connected"`
	Ready     bool              `json:"ready"`
	ACK       map[string]string `json:"ack"`
	NACK      string            `json:"nack"`
	LastSeen  time.Time         `json:"lastSeen"`
}
type Store struct {
	mu    sync.Mutex
	State State
	path  string
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &s.State)
		return s, err
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	s.State = State{Generation: 1, Cells: []Cell{{"shared-eu", "eu-local", false}, {"acme-eu", "eu-local", true}}, Revisions: []Revision{
		{"r41", "harbor-demo:local", 8080, "/health", map[string]string{"REVISION": "r41"}},
		{"r42", "harbor-demo:local", 8080, "/health", map[string]string{"REVISION": "r42"}},
		{"broken", "harbor-demo:local", 8080, "/health", map[string]string{"REVISION": "broken", "FAIL": "true"}},
	}, Deployments: []Deployment{
		{ID: "shared-r41", Cell: "shared-eu", Revision: "r41", Replicas: 2, Status: "pending"},
		{ID: "shared-r42", Cell: "shared-eu", Revision: "r42", Replicas: 2, Status: "pending"},
		{ID: "acme-r41", Cell: "acme-eu", Revision: "r41", Replicas: 2, Status: "pending"},
	}, Tenants: []Tenant{
		{ID: "smallco", Name: "SmallCo", Cell: "shared-eu", Domains: []string{"smallco.localhost"}, Deployment: "shared-r41"},
		{ID: "betaco", Name: "BetaCo", Cell: "shared-eu", Domains: []string{"betaco.localhost", "portal.betaco.test"}, Deployment: "shared-r41"},
		{ID: "acme", Name: "Acme", Cell: "acme-eu", Domains: []string{"acme.localhost", "app.acme.test"}, Deployment: "acme-r41", Approval: true},
	}, Rollouts: []Rollout{}, Events: []Event{}}
	s.event("system", "harbor", "Local environment initialized")
	return s, s.save()
}
func (s *Store) event(kind, subject, message string) {
	s.State.Events = append([]Event{{time.Now().UTC(), kind, subject, message}}, s.State.Events...)
	if len(s.State.Events) > 300 {
		s.State.Events = s.State.Events[:300]
	}
}
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.State, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), "state-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *Store) mustSave() {
	if err := s.save(); err != nil {
		panic(fmt.Sprintf("cannot persist control state: %v", err))
	}
}
func (s *Store) copy() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal(s.State)
	var c State
	json.Unmarshal(b, &c)
	return c
}
func (s *State) deployment(id string) *Deployment {
	for i := range s.Deployments {
		if s.Deployments[i].ID == id {
			return &s.Deployments[i]
		}
	}
	return nil
}
func (s *State) tenant(id string) *Tenant {
	for i := range s.Tenants {
		if s.Tenants[i].ID == id {
			return &s.Tenants[i]
		}
	}
	return nil
}
func (s *State) revision(id string) *Revision {
	for i := range s.Revisions {
		if s.Revisions[i].ID == id {
			return &s.Revisions[i]
		}
	}
	return nil
}
func (s *State) cell(id string) *Cell {
	for i := range s.Cells {
		if s.Cells[i].ID == id {
			return &s.Cells[i]
		}
	}
	return nil
}
func active(status string) bool {
	return status == "awaiting_approval" || status == "preparing" || status == "activating" || status == "observing" || status == "reverting"
}
