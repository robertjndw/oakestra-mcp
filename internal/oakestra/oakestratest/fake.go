// Package oakestratest is a stateful fake of the system manager endpoints
// the write tools use. It mimics the behaviour that matters to them:
// instance numbers come from a per-service counter and are never reused,
// POST /api/application answers with all of the user's applications, and
// deploy/undeploy answer 200 even when nothing happens.
package oakestratest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Instance is one entry of a service's instance_list.
type Instance struct {
	Number int    `json:"instance_number"`
	Status string `json:"status"`
}

// Service is a fake job document.
type Service struct {
	ID        string
	Name      string
	Namespace string
	AppID     string
	Status    string
	Next      int
	Instances []Instance
	// Job holds the scheduling fields (virtualization, memory, vcpus, ...).
	Job map[string]any
}

// App is a fake application.
type App struct {
	ID, Name, Namespace string
	Services            []string
}

// Fake is the system manager. Lock Mu when touching the maps from a test
// while requests may be in flight.
type Fake struct {
	Mu sync.Mutex
	// Token is a non-admin user's token; AdminToken, when set, is accepted
	// as an admin's.
	Token      string
	AdminToken string
	// User and UserID identify the owner of Token. Oakestra serves their
	// applications at GET /api/applications/<UserID>.
	User, UserID string
	Apps         map[string]*App
	Services     map[string]*Service
	// Calls counts requests as "METHOD /path".
	Calls map[string]int
	// FailDeployAt makes the n-th POST .../instance (1-based) answer 500.
	FailDeployAt int
	// AppsListForbidden makes both application listings answer 403.
	AppsListForbidden bool

	deploys int
	nextID  int
	URL     string
}

// New starts a fake with one application "shop" (app1) holding service
// "web" (svc1) with instance 0 running.
func New(t *testing.T, token string) *Fake {
	t.Helper()
	f := &Fake{
		Token: token, User: "alice", UserID: "user1",
		Apps: map[string]*App{"app1": {ID: "app1", Name: "shop", Namespace: "prod", Services: []string{"svc1"}}},
		Services: map[string]*Service{"svc1": {
			ID: "svc1", Name: "web", Namespace: "prod", AppID: "app1", Status: "RUNNING", Next: 1,
			Instances: []Instance{{Number: 0, Status: "RUNNING"}},
			Job:       map[string]any{"virtualization": "docker", "memory": 256, "vcpus": 1, "code": "nginx"},
		}},
		Calls:  map[string]int{},
		nextID: 100,
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.URL = srv.URL
	return f
}

// AddInstance appends an instance as if the scheduler placed one.
func (f *Fake) AddInstance(serviceID, status string) int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	s := f.Services[serviceID]
	n := s.Next
	s.Next++
	s.Instances = append(s.Instances, Instance{Number: n, Status: status})
	return n
}

// InstanceNumbers returns the service's instance numbers in list order.
func (f *Fake) InstanceNumbers(serviceID string) []int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	var ns []int
	for _, i := range f.Services[serviceID].Instances {
		ns = append(ns, i.Number)
	}
	return ns
}

// Count returns how often "METHOD /path" was called.
func (f *Fake) Count(key string) int {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Calls[key]
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	admin := f.AdminToken != "" && auth == "Bearer "+f.AdminToken
	if auth != "Bearer "+f.Token && !admin {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()
	path := strings.TrimSuffix(r.URL.Path, "/")
	f.Calls[r.Method+" "+path]++
	parts := strings.Split(strings.TrimPrefix(path, "/api/"), "/")

	switch {
	case r.Method == http.MethodGet && path == "/api/applications":
		// @require_role(Role.ADMIN) in the real system manager.
		if f.AppsListForbidden || !admin {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		writeJSON(w, f.appList())
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "applications":
		if f.AppsListForbidden {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// Anyone but the owner of the token gets 401, as in Oakestra.
		if parts[1] != f.UserID {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, f.appList())
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "user":
		if parts[1] != f.User {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"_id": map[string]string{"$oid": f.UserID}, "name": f.User})
	case r.Method == http.MethodPost && path == "/api/application":
		f.create(w, r)
	case len(parts) == 2 && parts[0] == "application":
		a := f.Apps[parts[1]]
		if a == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodDelete {
			for _, id := range a.Services {
				delete(f.Services, id)
			}
			delete(f.Apps, a.ID)
			writeJSON(w, map[string]string{"message": "Application Deleted"})
			return
		}
		writeJSON(w, appJSON(a))
	case r.Method == http.MethodGet && parts[0] == "services":
		var out []map[string]any
		for _, s := range f.sortedServices() {
			if len(parts) == 2 && s.AppID != parts[1] {
				continue
			}
			out = append(out, f.serviceJSON(s))
		}
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, out)
	case parts[0] == "service" && len(parts) >= 2:
		s := f.Services[parts[1]]
		switch {
		case len(parts) == 2 && r.Method == http.MethodGet:
			if s == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(w, f.serviceJSON(s))
		case len(parts) == 3 && r.Method == http.MethodPost:
			f.deploys++
			if f.deploys == f.FailDeployAt {
				http.Error(w, `{"message":"scheduler unreachable"}`, http.StatusInternalServerError)
				return
			}
			// Real Oakestra appends the instance only after the scheduler
			// answers; doing it right away keeps tests synchronous.
			if s != nil {
				s.Instances = append(s.Instances, Instance{Number: s.Next, Status: "CLUSTER_SCHEDULED"})
				s.Next++
			}
			writeJSON(w, map[string]string{"message": "ok"})
		case len(parts) == 4 && r.Method == http.MethodDelete:
			n, _ := strconv.Atoi(parts[3])
			if s != nil {
				s.Instances = slices.DeleteFunc(s.Instances, func(i Instance) bool { return i.Number == n })
			}
			writeJSON(w, map[string]string{"message": "ok"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *Fake) create(w http.ResponseWriter, r *http.Request) {
	var sla struct {
		Version      string `json:"sla_version"`
		Applications []struct {
			Name          string           `json:"application_name"`
			Namespace     string           `json:"application_namespace"`
			Microservices []map[string]any `json:"microservices"`
		} `json:"applications"`
	}
	if err := json.NewDecoder(r.Body).Decode(&sla); err != nil || sla.Version == "" {
		http.Error(w, `{"message":"'sla_version' is a required property"}`, http.StatusUnprocessableEntity)
		return
	}
	for _, a := range sla.Applications {
		for _, x := range f.Apps {
			if x.Name == a.Name && x.Namespace == a.Namespace {
				http.Error(w, `{"message":"An application with the same name and namespace exists already"}`, http.StatusConflict)
				return
			}
		}
		app := &App{ID: f.newID("app"), Name: a.Name, Namespace: a.Namespace}
		for _, ms := range a.Microservices {
			name, _ := ms["microservice_name"].(string)
			ns, _ := ms["microservice_namespace"].(string)
			s := &Service{ID: f.newID("svc"), Name: name, Namespace: ns, AppID: app.ID, Job: ms}
			f.Services[s.ID] = s
			app.Services = append(app.Services, s.ID)
		}
		f.Apps[app.ID] = app
	}
	writeJSON(w, f.appList())
}

func (f *Fake) newID(prefix string) string {
	f.nextID++
	return fmt.Sprintf("%s%d", prefix, f.nextID)
}

func (f *Fake) appList() []map[string]any {
	ids := make([]string, 0, len(f.Apps))
	for id := range f.Apps {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := []map[string]any{}
	for _, id := range ids {
		out = append(out, appJSON(f.Apps[id]))
	}
	return out
}

func appJSON(a *App) map[string]any {
	svcs := a.Services
	if svcs == nil {
		svcs = []string{}
	}
	return map[string]any{"applicationID": a.ID, "application_name": a.Name, "application_namespace": a.Namespace, "microservices": svcs}
}

func (f *Fake) sortedServices() []*Service {
	ids := make([]string, 0, len(f.Services))
	for id := range f.Services {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]*Service, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.Services[id])
	}
	return out
}

func (f *Fake) serviceJSON(s *Service) map[string]any {
	m := map[string]any{}
	for k, v := range s.Job {
		m[k] = v
	}
	app := f.Apps[s.AppID]
	insts := s.Instances
	if insts == nil {
		insts = []Instance{}
	}
	m["microserviceID"] = s.ID
	m["_id"] = s.ID
	m["microservice_name"] = s.Name
	m["microservice_namespace"] = s.Namespace
	m["applicationID"] = s.AppID
	if app != nil {
		m["app_name"] = app.Name
		m["application_name"] = app.Name
	}
	m["status"] = s.Status
	m["instance_list"] = insts
	m["next_instance_progressive_number"] = s.Next
	return m
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
