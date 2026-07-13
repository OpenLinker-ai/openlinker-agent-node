package agentnode

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const AgentNodeVersion = "openlinker-agent-node/reliable-run-v2"

type RuntimeV2Client interface {
	CreateRuntimeV2Session(context.Context, openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error)
	HeartbeatRuntimeV2Session(context.Context, openlinker.RuntimeV2HelloPayload) (*openlinker.RuntimeV2ReadyPayload, error)
	CloseRuntimeV2Session(context.Context, openlinker.RuntimeV2SessionCloseRequest) error
	ClaimRuntimeV2Run(context.Context, int, openlinker.RuntimeV2ClaimRequest) (*openlinker.RuntimeV2RunAssignedPayload, error)
	AckRuntimeV2Assignment(context.Context, openlinker.RuntimeV2AssignmentAckPayload) (*openlinker.RuntimeV2AssignmentConfirmedPayload, error)
	RejectRuntimeV2Assignment(context.Context, openlinker.RuntimeV2AssignmentRejectPayload) (*openlinker.RuntimeV2AssignmentRejectedPayload, error)
	RenewRuntimeV2Lease(context.Context, openlinker.RuntimeV2LeaseRenewPayload) (*openlinker.RuntimeV2LeaseRenewedPayload, error)
	AppendRuntimeV2Event(context.Context, openlinker.RuntimeV2RunEventPayload) (*openlinker.RuntimeV2RunEventAckPayload, error)
	FinalizeRuntimeV2Result(context.Context, openlinker.RuntimeV2RunResultPayload) (*openlinker.RuntimeV2RunResultAckPayload, error)
	ResumeRuntimeV2Runs(context.Context, openlinker.RuntimeV2ResumePayload) (*openlinker.RuntimeV2ResumeResponse, error)
	PollRuntimeV2Commands(context.Context, string, int) (*openlinker.RuntimeV2CommandsResponse, error)
	AckRuntimeV2Cancel(context.Context, openlinker.RuntimeV2RunCancelAckPayload) (*openlinker.RuntimeV2RunCancellationState, error)
	CallRuntimeV2Agent(context.Context, openlinker.RuntimeV2CallAgentAuthorization, openlinker.RuntimeV2CallAgentRequest) (*openlinker.RuntimeV2RunSummary, error)
}

type RuntimeMTLSConfig struct {
	RuntimeURL    string
	AgentToken    string
	CertFile      string
	KeyFile       string
	CAFile        string
	TLSServerName string
}

func newRuntimeV2Client(config RuntimeMTLSConfig) (*openlinker.Runtime, *http.Client, error) {
	runtimeURL, err := validateRuntimeOrigin(config.RuntimeURL)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(config.AgentToken) == "" {
		return nil, nil, errors.New("Agent Token is required")
	}
	if config.CertFile == "" || config.KeyFile == "" || config.CAFile == "" {
		return nil, nil, errors.New("runtime mTLS cert, key, and CA files are required")
	}
	certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load runtime mTLS client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read runtime mTLS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, nil, errors.New("runtime mTLS CA file contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
		ServerName:   strings.TrimSpace(config.TLSServerName),
	}
	transport.ResponseHeaderTimeout = 35 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Runtime credentials and the client certificate are bound to the
			// configured Core origin. Runtime endpoints must not redirect them.
			return http.ErrUseLastResponse
		},
	}
	runtimeClient, err := openlinker.NewRuntime(
		runtimeURL,
		openlinker.WithAgentToken(config.AgentToken),
		openlinker.WithHTTPClient(httpClient),
		openlinker.WithSDKAgent(AgentNodeVersion),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	return runtimeClient, httpClient, nil
}
