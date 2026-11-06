// Package tailnettest is a fake of the tailnet API for tests. It keeps the two
// credentials apart the way the real one does: a token minted for the key
// client reaches keys and devices only, and one minted for the service client
// reaches services only.
package tailnettest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

// The credentials the fake accepts.
const (
	Tailnet             = "example.com"
	Domain              = "tail1234.ts.net"
	KeyClientID         = "key-client"
	KeyClientSecret     = "key-client-secret"
	ServiceClientID     = "service-client"
	ServiceClientSecret = "service-client-secret"
)

// Key is an auth key the fake minted.
type Key struct {
	ID            string
	Secret        string
	Tags          []string
	Ephemeral     bool
	Reusable      bool
	Preauthorized bool
	ExpirySeconds int
	Revoked       bool
}

// Device is a node on the fake tailnet.
type Device struct {
	ID       string
	Hostname string
	Tags     []string
}

// Approval is what the tailnet answers about a device hosting a private
// address: whether a person approved it and whether the policy does by itself.
// The real API answers both for any device, advertising or not.
type Approval struct {
	Approved     bool
	AutoApproved bool
}

type approvalKey struct{ service, deviceID string }

// Service is a private address on the fake tailnet.
type Service struct {
	Name  string
	Tags  []string
	Ports []string
}

// Server is the fake. Its fields are read and written under its lock through
// the methods below.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	tokens   map[string]string
	keys     []*Key
	devices  []Device
	services map[string]Service
	approved map[approvalKey]Approval
	failing  bool
	requests []string
}

// New starts a fake tailnet and stops it when the test ends.
func New(t *testing.T) *Server {
	t.Helper()

	s := &Server{tokens: map[string]string{}, services: map[string]Service{}, approved: map[approvalKey]Approval{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)

	return s
}

// Fail makes every call after this one answer 500, for a tailnet that is down.
func (s *Server) Fail() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failing = true
}

// Recover makes the fake answer again after Fail.
func (s *Server) Recover() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failing = false
}

// AddDevice puts a node on the tailnet, carrying tags if any are given.
func (s *Server) AddDevice(id, hostname string, tags ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.devices = append(s.devices, Device{ID: id, Hostname: hostname, Tags: tags})
}

// Approve sets what the tailnet answers about a device hosting a private
// address. A device never approved is answered as neither approved nor
// auto-approved.
func (s *Server) Approve(service, deviceID string, approval Approval) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.approved[approvalKey{service, deviceID}] = approval
}

// Devices are the nodes still on the tailnet.
func (s *Server) Devices() []Device {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]Device(nil), s.devices...)
}

// Keys are every key minted, revoked ones included.
func (s *Server) Keys() []Key {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Key, 0, len(s.keys))
	for _, key := range s.keys {
		out = append(out, *key)
	}

	return out
}

// SetService creates or replaces a private address, as someone editing the
// tailnet by hand would.
func (s *Server) SetService(svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.services[svc.Name] = svc
}

// Services are the private addresses that exist.
func (s *Server) Services() []Service {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Service, 0, len(s.services))
	for _, svc := range s.services {
		out = append(out, svc)
	}

	return out
}

// Requests are the "METHOD path" of every call after authentication, so a test
// can say a call was never made.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if r.URL.Path == "/api/v2/oauth/token" {
		s.token(w, r)

		return
	}
	if s.failing {
		http.Error(w, "the tailnet is down", http.StatusInternalServerError)

		return
	}
	who := s.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if who == "" {
		http.Error(w, "no valid token", http.StatusUnauthorized)

		return
	}
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)

	base := "/api/v2/tailnet/" + Tailnet
	switch path := r.URL.Path; {
	case path == base+"/vip-services" && r.Method == http.MethodGet:
		s.listServices(w, who)
	case strings.HasPrefix(path, base+"/vip-services/") && strings.HasSuffix(path, "/approved") && r.Method == http.MethodGet:
		s.deviceApproval(w, who, strings.TrimSuffix(strings.TrimPrefix(path, base+"/vip-services/"), "/approved"))
	case strings.HasPrefix(path, base+"/vip-services/"):
		s.service(w, r, who, strings.TrimPrefix(path, base+"/vip-services/"))
	case path == base+"/keys" && r.Method == http.MethodPost:
		s.mintKey(w, r, who)
	case strings.HasPrefix(path, base+"/keys/") && r.Method == http.MethodDelete:
		s.revokeKey(w, who, strings.TrimPrefix(path, base+"/keys/"))
	case path == base+"/devices" && r.Method == http.MethodGet:
		s.listDevices(w, who)
	case strings.HasPrefix(path, "/api/v2/device/") && r.Method == http.MethodDelete:
		s.deleteDevice(w, who, strings.TrimPrefix(path, "/api/v2/device/"))
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" {
		http.Error(w, "unsupported grant", http.StatusBadRequest)

		return
	}
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	var who string
	switch {
	case id == KeyClientID && secret == KeyClientSecret:
		who = KeyClientID
	case id == ServiceClientID && secret == ServiceClientSecret:
		who = ServiceClientID
	default:
		http.Error(w, "invalid client", http.StatusUnauthorized)

		return
	}
	token := fmt.Sprintf("token-%d", len(s.tokens)+1)
	s.tokens[token] = who

	writeJSON(w, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
}

func (s *Server) mintKey(w http.ResponseWriter, r *http.Request, who string) {
	if who != KeyClientID {
		http.Error(w, "this client cannot mint keys", http.StatusForbidden)

		return
	}
	var in struct {
		Capabilities struct {
			Devices struct {
				Create struct {
					Reusable      bool     `json:"reusable"`
					Ephemeral     bool     `json:"ephemeral"`
					Preauthorized bool     `json:"preauthorized"`
					Tags          []string `json:"tags"`
				} `json:"create"`
			} `json:"devices"`
		} `json:"capabilities"`
		ExpirySeconds int `json:"expirySeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)

		return
	}
	create := in.Capabilities.Devices.Create
	key := &Key{
		ID:            fmt.Sprintf("key-%d", len(s.keys)+1),
		Secret:        fmt.Sprintf("tskey-auth-secret-%d", len(s.keys)+1),
		Tags:          create.Tags,
		Ephemeral:     create.Ephemeral,
		Reusable:      create.Reusable,
		Preauthorized: create.Preauthorized,
		ExpirySeconds: in.ExpirySeconds,
	}
	s.keys = append(s.keys, key)

	writeJSON(w, map[string]any{"id": key.ID, "key": key.Secret})
}

func (s *Server) revokeKey(w http.ResponseWriter, who, id string) {
	if who != KeyClientID {
		http.Error(w, "this client cannot revoke keys", http.StatusForbidden)

		return
	}
	for _, key := range s.keys {
		if key.ID == id && !key.Revoked {
			key.Revoked = true
			w.WriteHeader(http.StatusOK)

			return
		}
	}
	http.Error(w, "no such key", http.StatusNotFound)
}

func (s *Server) listDevices(w http.ResponseWriter, who string) {
	if who != KeyClientID {
		http.Error(w, "this client cannot read devices", http.StatusForbidden)

		return
	}
	nodes := make([]map[string]any, 0, len(s.devices))
	for _, d := range s.devices {
		nodes = append(nodes, map[string]any{"id": d.ID, "hostname": d.Hostname, "tags": d.Tags})
	}

	writeJSON(w, map[string]any{"devices": nodes})
}

func (s *Server) deleteDevice(w http.ResponseWriter, who, id string) {
	if who != KeyClientID {
		http.Error(w, "this client cannot delete devices", http.StatusForbidden)

		return
	}
	for i, d := range s.devices {
		if d.ID == id {
			s.devices = append(s.devices[:i], s.devices[i+1:]...)
			w.WriteHeader(http.StatusOK)

			return
		}
	}
	http.Error(w, "no such device", http.StatusNotFound)
}

func (s *Server) listServices(w http.ResponseWriter, who string) {
	if who != ServiceClientID {
		http.Error(w, "this client cannot reach services", http.StatusForbidden)

		return
	}
	names := make([]string, 0, len(s.services))
	for name := range s.services {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		svc := s.services[name]
		out = append(out, map[string]any{"name": svc.Name, "tags": svc.Tags, "ports": svc.Ports})
	}

	writeJSON(w, map[string]any{"vipServices": out})
}

// deviceApproval answers whether a device may host a private address, for the
// path "<service>/device/<device id>". It answers for a device whether or not
// it advertises, and 404 for a service or a device the tailnet does not have.
func (s *Server) deviceApproval(w http.ResponseWriter, who, rest string) {
	if who != ServiceClientID {
		http.Error(w, "this client cannot reach services", http.StatusForbidden)

		return
	}
	escaped, deviceID, ok := strings.Cut(rest, "/device/")
	name, err := url.PathUnescape(escaped)
	if !ok || err != nil {
		http.Error(w, "no such path", http.StatusNotFound)

		return
	}
	_, serviceExists := s.services[name]
	deviceExists := slices.ContainsFunc(s.devices, func(d Device) bool { return d.ID == deviceID })
	if !serviceExists || !deviceExists {
		http.Error(w, "no such service or device", http.StatusNotFound)

		return
	}
	approval := s.approved[approvalKey{name, deviceID}]

	writeJSON(w, map[string]any{"approved": approval.Approved, "autoApproved": approval.AutoApproved})
}

func (s *Server) service(w http.ResponseWriter, r *http.Request, who, escaped string) {
	if who != ServiceClientID {
		http.Error(w, "this client cannot reach services", http.StatusForbidden)

		return
	}
	name, err := url.PathUnescape(escaped)
	if err != nil {
		http.Error(w, "bad name", http.StatusBadRequest)

		return
	}
	switch r.Method {
	case http.MethodGet:
		svc, ok := s.services[name]
		if !ok {
			http.Error(w, "no such service", http.StatusNotFound)

			return
		}
		writeJSON(w, map[string]any{"name": svc.Name, "tags": svc.Tags, "ports": svc.Ports})
	case http.MethodPut:
		var in struct {
			Tags  []string `json:"tags"`
			Ports []string `json:"ports"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)

			return
		}
		s.services[name] = Service{Name: name, Tags: in.Tags, Ports: in.Ports}
		writeJSON(w, map[string]any{"name": name, "tags": in.Tags, "ports": in.Ports})
	case http.MethodDelete:
		if _, ok := s.services[name]; !ok {
			http.Error(w, "no such service", http.StatusNotFound)

			return
		}
		delete(s.services, name)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
