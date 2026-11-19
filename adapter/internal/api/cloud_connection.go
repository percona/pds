package api

import (
	_ "embed"
	"errors"
	"net/http"
	"strings"

	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// cloudConnectionTemplate is the CloudFormation template a customer's AWS
// account runs once per cloud connection. It is generic: the connection's id
// and secret arrive as stack parameters, so the control plane serves the same
// text to everybody.
//
//go:embed cloud_connection_template.yaml
var cloudConnectionTemplate string

// cloudConnectionScript is the script a customer pipes into bash in AWS
// CloudShell. Only the validated hub URL is substituted into it.
//
//go:embed cloud_connection_connect.sh
var cloudConnectionScript string

const cloudRoutePrefix = "/api/internal/cloud-connection/"

type cloudEnrollRequest struct {
	AccountID      string   `json:"accountId" validate:"required"`
	Alias          string   `json:"alias"`
	Region         string   `json:"region" validate:"required"`
	AllowedRegions []string `json:"allowedRegions"`
}

type cloudEnrollResponse struct {
	ConnectionID    string   `json:"connectionId"`
	ExternalID      string   `json:"externalId"`
	StackName       string   `json:"stackName"`
	StackRegion     string   `json:"stackRegion"`
	AllowedRegions  []string `json:"allowedRegions"`
	PrincipalARN    string   `json:"principalArn"`
	TemplateURL     string   `json:"templateUrl"`
	TemplateVersion int      `json:"templateVersion"`
}

type cloudEventRequest struct {
	Step   string `json:"step" validate:"required"`
	Status string `json:"status" validate:"required"`
	Detail string `json:"detail"`
}

type cloudVerifyResponse struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// handleCloudTemplate serves the generic bootstrap template.
func (s *Server) handleCloudTemplate(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(cloudConnectionTemplate))
}

// handleCloudConnectScript serves the connect script with this control plane's
// address in it.
func (s *Server) handleCloudConnectScript(w http.ResponseWriter, r *http.Request) {
	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(strings.ReplaceAll(cloudConnectionScript, "__HUB_URL__", hubURL)))
}

// handleCloudEnroll names the account the script runs in and answers with what
// it needs to create the stack. The connect token is the only credential.
func (s *Server) handleCloudEnroll(w http.ResponseWriter, r *http.Request) {
	cloud, ok := s.cloudService(w, r)
	if !ok {
		return
	}
	var req cloudEnrollRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	hubURL, err := s.scriptOrigin(r)
	if err != nil {
		Respond(w, r, nil, err)

		return
	}
	got, err := cloud.Enroll(r.Context(), bearerToken(r), service.CloudAccount{
		ID: req.AccountID, Alias: req.Alias, Region: req.Region, Regions: req.AllowedRegions,
	})
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}
	Respond(w, r, cloudEnrollResponse{
		ConnectionID: got.ConnectionID, ExternalID: got.ExternalID, StackName: got.StackName, StackRegion: got.StackRegion,
		AllowedRegions: got.AllowedRegions, PrincipalARN: got.PrincipalARN,
		TemplateURL: hubURL + cloudRoutePrefix + "template.yaml", TemplateVersion: got.TemplateVersion,
	}, nil)
}

// handleCloudEvent keeps a step the script reported.
func (s *Server) handleCloudEvent(w http.ResponseWriter, r *http.Request) {
	cloud, ok := s.cloudService(w, r)
	if !ok {
		return
	}
	var req cloudEventRequest
	if err := s.decode(r, &req); err != nil {
		Respond(w, r, nil, err)

		return
	}
	err := cloud.RecordEvent(r.Context(), bearerToken(r), service.CloudStep{Step: req.Step, Status: req.Status, Detail: req.Detail})
	Respond(w, r, nil, cloudError(err))
}

// handleCloudVerify runs the connection check for the token's connection.
func (s *Server) handleCloudVerify(w http.ResponseWriter, r *http.Request) {
	cloud, ok := s.cloudService(w, r)
	if !ok {
		return
	}
	row, err := cloud.VerifyWithToken(r.Context(), bearerToken(r))
	if err != nil {
		Respond(w, r, nil, cloudError(err))

		return
	}
	Respond(w, r, cloudVerifyResponse{State: row.State, Reason: row.Reason}, nil)
}

// cloudService answers 501 where this deployment has no cloud connections.
func (s *Server) cloudService(w http.ResponseWriter, r *http.Request) (*service.CloudConnections, bool) {
	if s.cloud == nil {
		Respond(w, r, nil, cloudError(service.ErrCloudUnavailable))

		return nil, false
	}

	return s.cloud, true
}

// cloudError maps what the service and the store refuse onto the API's errors.
// It returns nil for nil, so a handler can pass the result straight to Respond.
func cloudError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrCloudTokenInvalid):
		return Unauthorized("That connect token is not valid. Create a new one in the console.").WithCause(err)
	case errors.Is(err, store.ErrCloudTokenAccount):
		return Conflict("That connect token already belongs to another AWS account. Create a new one in the console.").WithCause(err)
	case errors.Is(err, store.ErrCloudConnectionUnknown):
		return NotFound("That AWS account is not connected.").WithCause(err)
	case errors.Is(err, store.ErrCloudConnectionReady):
		return Conflict("That AWS account was connected before. Delete its stack to disconnect it.").WithCause(err)
	case errors.Is(err, service.ErrCloudInvalid):
		return Validation("Check the AWS account and the regions, then run the command again.").WithCause(err)
	case errors.Is(err, service.ErrCloudUnavailable):
		return NotImplemented("This control plane is not set up to connect AWS accounts. Contact Percona support.").WithCause(err)
	}

	return Internal("The AWS account could not be connected. Try again.", err)
}
