package agentnode

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

const (
	DefaultCapacity          int64 = 1
	DefaultClaimWait               = 25 * time.Second
	DefaultCommandWait             = 25 * time.Second
	DefaultHeartbeatInterval       = 5 * time.Second
	DefaultRetryMinimum            = 250 * time.Millisecond
	DefaultRetryMaximum            = 15 * time.Second
)

type Node struct {
	CoreURL    string
	NodeID     string
	AgentID    string
	AgentToken string
	DataDir    string

	MTLSCertFile   string
	MTLSKeyFile    string
	MTLSCAFile     string
	MTLSServerName string

	Capacity          int64
	ClaimWait         time.Duration
	CommandWait       time.Duration
	HeartbeatInterval time.Duration
	RetryMinimum      time.Duration
	RetryMaximum      time.Duration

	Adapter   Adapter
	Helper    *LocalHelperServer
	PublicA2A *PublicA2AServer
	Logger    *log.Logger

	// RuntimeClient is an injection seam for deterministic tests. Production
	// configuration leaves it nil and always builds a TLS 1.3 mTLS client.
	RuntimeClient RuntimeV2Client

	lifecycleMu sync.Mutex
	started     bool
	done        chan struct{}
	runtimeCtx  context.Context
	runtimeStop context.CancelFunc
	httpClient  *http.Client
	store       *RuntimeDurableStore
	ready       *openlinker.RuntimeV2ReadyPayload

	stateMu       sync.RWMutex
	draining      bool
	active        map[string]*activeRuntimeAttempt
	cancellations map[string]struct{}
	spoolAllowed  map[string]spoolPermission
	wakeSpool     chan struct{}
	fatal         chan error
	stopRequest   chan struct{}
	stopRequested bool
	loops         sync.WaitGroup
	executions    sync.WaitGroup

	jitter func(time.Duration) time.Duration
}

func (node *Node) Start(parent context.Context) (retErr error) {
	if parent == nil {
		parent = context.Background()
	}
	if err := node.applyDefaultsAndValidate(); err != nil {
		return err
	}
	if err := node.beginLifecycle(); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), DefaultShutdownTimeout)
		defer cancel()
		shutdownErr := node.shutdown(shutdownCtx)
		if retErr == nil && shutdownErr != nil {
			retErr = shutdownErr
		}
	}()
	startupCtx, cancelStartup := context.WithCancel(parent)
	defer cancelStartup()
	go func() {
		select {
		case <-node.stopRequest:
			cancelStartup()
		case <-startupCtx.Done():
		}
	}()

	store, err := OpenRuntimeDurableStore(node.DataDir)
	if err != nil {
		return err
	}
	node.store = store

	if node.RuntimeClient == nil {
		runtimeClient, httpClient, err := newRuntimeV2Client(RuntimeMTLSConfig{
			CoreURL:       node.CoreURL,
			AgentToken:    node.AgentToken,
			CertFile:      node.MTLSCertFile,
			KeyFile:       node.MTLSKeyFile,
			CAFile:        node.MTLSCAFile,
			TLSServerName: node.MTLSServerName,
		})
		if err != nil {
			return err
		}
		node.RuntimeClient = runtimeClient
		node.httpClient = httpClient
	}

	if node.Helper != nil {
		if err := node.Helper.Start(node.runtimeCtx); err != nil {
			return err
		}
	}
	if node.PublicA2A != nil {
		if node.PublicA2A.Adapter == nil {
			node.PublicA2A.Adapter = node.Adapter
		}
		if err := node.PublicA2A.Start(node.runtimeCtx); err != nil {
			return err
		}
	}

	ready, err := node.createSessionWithRetry(startupCtx)
	if err != nil {
		return err
	}
	node.stateMu.Lock()
	node.ready = ready
	node.stateMu.Unlock()
	if err := node.resumeDurableState(startupCtx); err != nil {
		return err
	}

	node.startRuntimeLoops()
	select {
	case <-parent.Done():
		return nil
	case <-node.stopRequest:
		return nil
	case err := <-node.fatal:
		return err
	}
}

func (node *Node) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	node.lifecycleMu.Lock()
	if !node.started {
		node.lifecycleMu.Unlock()
		return nil
	}
	done := node.done
	node.setDraining(true)
	if !node.stopRequested {
		close(node.stopRequest)
		node.stopRequested = true
	}
	node.lifecycleMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (node *Node) beginLifecycle() error {
	node.lifecycleMu.Lock()
	defer node.lifecycleMu.Unlock()
	if node.started {
		return errors.New("agent node is already started")
	}
	node.started = true
	node.done = make(chan struct{})
	node.runtimeCtx, node.runtimeStop = context.WithCancel(context.Background())
	node.active = make(map[string]*activeRuntimeAttempt)
	node.cancellations = make(map[string]struct{})
	node.spoolAllowed = make(map[string]spoolPermission)
	node.wakeSpool = make(chan struct{}, 1)
	node.fatal = make(chan error, 1)
	node.stopRequest = make(chan struct{})
	node.stopRequested = false
	node.draining = false
	node.ready = nil
	return nil
}

func (node *Node) applyDefaultsAndValidate() error {
	if node.CoreURL == "" {
		return errors.New("Core v2 URL is required")
	}
	if !validRuntimeUUID(node.NodeID) {
		return errors.New("Node ID must be a non-zero lowercase UUID")
	}
	if !validRuntimeUUID(node.AgentID) {
		return errors.New("Agent ID must be a non-zero lowercase UUID")
	}
	if node.AgentToken == "" && node.RuntimeClient == nil {
		return errors.New("Agent Token is required")
	}
	if node.DataDir == "" {
		return errors.New("runtime data directory is required")
	}
	if node.Adapter == nil {
		return errors.New("adapter is required")
	}
	if node.RuntimeClient == nil && (node.MTLSCertFile == "" || node.MTLSKeyFile == "" || node.MTLSCAFile == "") {
		return errors.New("runtime mTLS cert, key, and CA files are required")
	}
	if node.Capacity == 0 {
		node.Capacity = DefaultCapacity
	}
	if node.Capacity < 1 || node.Capacity > openlinker.RuntimeV2MaxNodeCapacity {
		return fmt.Errorf("capacity must be between 1 and %d", openlinker.RuntimeV2MaxNodeCapacity)
	}
	if node.ClaimWait <= 0 {
		node.ClaimWait = DefaultClaimWait
	}
	if node.CommandWait <= 0 {
		node.CommandWait = DefaultCommandWait
	}
	if node.HeartbeatInterval <= 0 {
		node.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if node.RetryMinimum <= 0 {
		node.RetryMinimum = DefaultRetryMinimum
	}
	if node.RetryMaximum <= 0 {
		node.RetryMaximum = DefaultRetryMaximum
	}
	if node.RetryMaximum < node.RetryMinimum {
		return errors.New("retry maximum must not be less than retry minimum")
	}
	return nil
}

func (node *Node) startRuntimeLoops() {
	for _, loop := range []func(){node.claimLoop, node.commandLoop, node.heartbeatLoop, node.spoolLoop} {
		node.loops.Add(1)
		go func(run func()) {
			defer node.loops.Done()
			run()
		}(loop)
	}
}

func (node *Node) shutdown(ctx context.Context) error {
	node.setDraining(true)
	heartbeatCtx, cancelHeartbeat := context.WithTimeout(ctx, 2*time.Second)
	_ = node.heartbeatOnce(heartbeatCtx)
	cancelHeartbeat()

	executionsDone := make(chan struct{})
	go func() {
		node.executions.Wait()
		close(executionsDone)
	}()
	select {
	case <-executionsDone:
	case <-ctx.Done():
		node.cancelAllActive()
		forceTimer := time.NewTimer(2 * time.Second)
		select {
		case <-executionsDone:
			forceTimer.Stop()
		case <-forceTimer.C:
		}
	}

	if node.store != nil && node.RuntimeClient != nil {
		identity := node.store.Identity()
		_ = node.RuntimeClient.CloseRuntimeV2Session(ctx, openlinker.RuntimeV2SessionCloseRequest{
			NodeID:           node.NodeID,
			AgentID:          node.AgentID,
			WorkerID:         identity.WorkerID,
			RuntimeSessionID: identity.RuntimeSessionID,
			SessionEpoch:     identity.SessionEpoch,
			Status:           "closed",
			Reason:           "node_shutdown",
		})
	}
	node.cancelAllActive()
	if node.runtimeStop != nil {
		node.runtimeStop()
	}
	node.loops.Wait()

	var firstErr error
	if node.Helper != nil {
		firstErr = node.Helper.Stop(ctx)
	}
	if node.PublicA2A != nil {
		if err := node.PublicA2A.Stop(ctx); firstErr == nil {
			firstErr = err
		}
	}
	if node.store != nil {
		if err := node.store.Close(); firstErr == nil {
			firstErr = err
		}
		node.store = nil
	}
	if node.httpClient != nil {
		if transport, ok := node.httpClient.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}

	node.lifecycleMu.Lock()
	if node.started {
		node.started = false
		close(node.done)
	}
	node.lifecycleMu.Unlock()
	return firstErr
}

func (node *Node) setDraining(value bool) {
	node.stateMu.Lock()
	node.draining = value
	node.stateMu.Unlock()
}

func (node *Node) isDraining() bool {
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	return node.draining
}

func (node *Node) capacitySnapshot() (capacity, inflight int64) {
	node.stateMu.RLock()
	defer node.stateMu.RUnlock()
	if !node.draining {
		capacity = node.Capacity
	}
	return capacity, int64(len(node.active))
}

func (node *Node) signalSpool() {
	select {
	case node.wakeSpool <- struct{}{}:
	default:
	}
}

func (node *Node) reportFatal(err error) {
	if err == nil {
		return
	}
	select {
	case node.fatal <- err:
	default:
	}
}

func (node *Node) logf(format string, args ...any) {
	if node.Logger != nil {
		node.Logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}
