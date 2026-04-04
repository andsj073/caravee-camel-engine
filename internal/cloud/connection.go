package cloud

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/caravee/engine/internal/camel"
	"github.com/caravee/engine/internal/config"
	"github.com/caravee/engine/internal/deploy"
	"github.com/caravee/engine/internal/monitor"
	"github.com/caravee/engine/internal/pairing"
	"github.com/caravee/engine/internal/runlog"
	"github.com/caravee/engine/internal/system"
	"github.com/gorilla/websocket"
)

const (
	maxReconnectDelay = 300 * time.Second
	pingInterval      = 30 * time.Second
	writeTimeout      = 10 * time.Second
)

// runState tracks an in-progress or recently completed run in memory.
type runState struct {
	runID     string
	startedAt time.Time
	revision  int
	exchanges int64
}

// Connection manages the WSS link to Caravee Cloud.
type Connection struct {
	cfg          *config.CloudConfig
	identity     *config.Identity
	deployer     *deploy.Deployer
	camel        *camel.Client
	privKey      *rsa.PrivateKey // For decrypting secrets
	ws           *websocket.Conn
	mu           sync.Mutex
	done         chan struct{}
	startAt      time.Time
	agentVersion string

	runStoreOnce sync.Once
	runStoreInst *runlog.Store
	runsMu       sync.Mutex
	currentRuns  map[string]*runState // integrationID → active run
}

// NewConnection creates a new cloud connection.
func NewConnection(cfg *config.CloudConfig, identity *config.Identity, deployer *deploy.Deployer, camelClient *camel.Client, agentVersion ...string) *Connection {
	// Load private key for secret decryption
	privKey, err := pairing.LoadPrivateKey(identity.DataDir)
	if err != nil {
		slog.Warn("Failed to load private key — secrets decryption unavailable", "error", err)
		privKey = nil
	}

	ver := "dev"
	if len(agentVersion) > 0 && agentVersion[0] != "" {
		ver = agentVersion[0]
	}
	return &Connection{
		cfg:          cfg,
		identity:     identity,
		deployer:     deployer,
		camel:        camelClient,
		privKey:      privKey,
		done:         make(chan struct{}),
		startAt:      time.Now(),
		currentRuns:  map[string]*runState{},
		agentVersion: ver,
	}
}

// Run connects and handles messages. Blocks until permanently closed.
func (c *Connection) Run() error {
	// Start error monitor (polls Camel metrics, pushes route_error events)
	mon := monitor.New(c.camel, c)
	mon.Start()
	defer mon.Stop()

	attempt := 0
	for {
		select {
		case <-c.done:
			return nil
		default:
		}

		err := c.connectAndServe()
		if err != nil {
			slog.Warn("Connection lost", "error", err, "attempt", attempt)
		}

		select {
		case <-c.done:
			return nil
		default:
		}

		// Exponential backoff
		delay := time.Duration(math.Min(float64(time.Second)*math.Pow(2, float64(attempt)), float64(maxReconnectDelay)))
		slog.Info("Reconnecting", "delay", delay)
		time.Sleep(delay)
		attempt++
	}
}

// Close shuts down the connection.
func (c *Connection) Close() {
	close(c.done)
	c.mu.Lock()
	if c.ws != nil {
		c.ws.Close()
	}
	c.mu.Unlock()
}

func (c *Connection) connectAndServe() error {
	header := http.Header{}
	header.Set("X-Engine-ID", c.identity.EngineID)
	header.Set("X-Tenant-ID", c.cfg.TenantID)

	slog.Info("Connecting to cloud", "url", c.cfg.WSSURL)
	ws, _, err := websocket.DefaultDialer.Dial(c.cfg.WSSURL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	c.mu.Lock()
	c.ws = ws
	c.mu.Unlock()

	slog.Info("Connected to cloud — awaiting auth challenge")

	// ── RSA challenge-response authentication ──
	if err := c.authenticate(ws); err != nil {
		ws.Close()
		return fmt.Errorf("auth: %w", err)
	}

	// Send connected message
	deployed, err := c.deployer.ListDeployed()
	if err != nil {
		slog.Warn("Could not list deployed routes", "error", err)
		deployed = []string{}
	}

	// Collect local vars with source info
	rawLocalVars := c.deployer.ListLocalVars()
	localVars := make([]LocalVar, len(rawLocalVars))
	for i, v := range rawLocalVars {
		localVars[i] = LocalVar{Name: v.Name, Source: v.Source}
	}

	// Gather runtime info from Camel sidecar (best-effort)
	camelVersion := c.camel.GetCamelVersion()
	var camelUptime float64
	if m, err := c.camel.GetEngineMetrics(); err == nil {
		camelUptime = m["process_uptime_seconds"]
	}

	c.sendMessage(&ConnectedMessage{
		Type:         MsgTypeConnected,
		EngineID:     c.identity.EngineID,
		Version:      c.agentVersion,
		CamelVersion: camelVersion,
		OS:           runtime.GOOS + "/" + runtime.GOARCH,
		Uptime:       camelUptime,
		Metadata: map[string]string{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		DeployedRoutes: deployed,
		LocalVars:      localVars,
	})
	slog.Info("Reported deployed routes", "count", len(deployed), "routes", deployed)
	slog.Info("Reported local vars", "count", len(localVars))

	// Keepalive: ping every 30s, expect pong within 10s.
	// Detects silent disconnects (e.g. backend restart without close frame).
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(pingInterval + 10*time.Second))
		return nil
	})
	ws.SetReadDeadline(time.Now().Add(pingInterval + 10*time.Second))

	// Ping ticker in background
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.mu.Lock()
				ws := c.ws
				c.mu.Unlock()
				if ws != nil {
					ws.SetWriteDeadline(time.Now().Add(writeTimeout))
					if err := ws.WriteMessage(websocket.PingMessage, nil); err != nil {
						return
					}
				}
			case <-pingDone:
				return
			}
		}
	}()
	defer close(pingDone)

	// Message loop
	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		var msg InboundMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			slog.Warn("Invalid message", "error", err)
			continue
		}
		msg.Raw = data

		go c.handleMessage(msg)
	}
}

// authenticate handles the RSA challenge-response handshake.
// Cloud sends {"type":"auth_challenge","nonce":"<uuid>"}, agent signs the nonce
// with its private key and replies with {"type":"auth_response","engine_id":"...","signature":"<base64>"}.
// Cloud verifies and replies with {"type":"auth_ok"} or closes the connection.
func (c *Connection) authenticate(ws *websocket.Conn) error {
	if c.privKey == nil {
		return fmt.Errorf("private key not loaded — cannot authenticate")
	}

	// Read auth_challenge (30s timeout matches backend)
	ws.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("reading auth_challenge: %w", err)
	}

	var challenge struct {
		Type  string `json:"type"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(data, &challenge); err != nil || challenge.Type != "auth_challenge" {
		return fmt.Errorf("expected auth_challenge, got: %s", string(data))
	}

	// Sign nonce with RSA-PKCS1v15-SHA256
	hash := crypto.SHA256.New()
	hash.Write([]byte(challenge.Nonce))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privKey, crypto.SHA256, hash.Sum(nil))
	if err != nil {
		return fmt.Errorf("signing nonce: %w", err)
	}

	// Send auth_response
	resp := map[string]string{
		"type":      "auth_response",
		"engine_id": c.identity.EngineID,
		"signature": base64.StdEncoding.EncodeToString(signature),
	}
	ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := ws.WriteJSON(resp); err != nil {
		return fmt.Errorf("sending auth_response: %w", err)
	}
	slog.Info("Auth challenge signed, sent auth_response")

	// Wait for auth_ok (10s)
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err = ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("reading auth_ok: %w", err)
	}

	var ack struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &ack); err != nil || ack.Type != "auth_ok" {
		return fmt.Errorf("expected auth_ok, got: %s", string(data))
	}

	// Clear deadline for normal operation
	ws.SetReadDeadline(time.Time{})

	slog.Info("Authentication successful")
	return nil
}

func (c *Connection) handleMessage(msg InboundMessage) {
	slog.Debug("Received message", "type", msg.Type, "request_id", msg.RequestID)

	switch msg.Type {
	case MsgTypeDeploy:
		var dm DeployMessage
		if err := json.Unmarshal(msg.Raw, &dm); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		c.handleDeploy(dm)

	case MsgTypeGetEngineMetrics:
		var req GetEngineMetricsMessage
		json.Unmarshal(msg.Raw, &req)
		go c.handleGetEngineMetrics(req)

	case MsgTypeGetRouteMetrics:
		var req GetRouteMetricsMessage
		json.Unmarshal(msg.Raw, &req)
		go c.handleGetRouteMetrics(req)

	case MsgTypeCheckVars:
		var cv CheckVarsMessage
		if err := json.Unmarshal(msg.Raw, &cv); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		c.handleCheckVars(cv)

	case MsgTypeSuspendRoute, MsgTypeResumeRoute, MsgTypeRouteStatus:
		var cmd RouteCommandMessage
		if err := json.Unmarshal(msg.Raw, &cmd); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		c.handleRouteCommand(msg.Type, cmd)

	case MsgTypeUndeploy:
		var um UndeployMessage
		if err := json.Unmarshal(msg.Raw, &um); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		c.handleUndeploy(um)

	case MsgTypePing:
		c.sendMessage(&PongMessage{
			Type:          MsgTypePong,
			EngineID:      c.identity.EngineID,
			UptimeSeconds: int64(time.Since(c.startAt).Seconds()),
		})

	case MsgTypeTelemetry:
		c.handleTelemetry(msg.RequestID)

	case MsgTypeSetLabel:
		var raw map[string]string
		json.Unmarshal(msg.Raw, &raw)
		if label, ok := raw["label"]; ok {
			c.cfg.Label = label
			slog.Info("Label updated", "label", label)
		}

	case MsgTypeGetHTTPPaths:
		var req GetHTTPPathsMessage
		json.Unmarshal(msg.Raw, &req)
		go c.handleGetHTTPPaths(req)

	case MsgTypeGetRunHistory:
		var req GetRunHistoryMessage
		if err := json.Unmarshal(msg.Raw, &req); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		go c.handleGetRunHistory(req)

	case MsgTypeDeployTest:
		var req DeployTestMessage
		if err := json.Unmarshal(msg.Raw, &req); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		go c.handleDeployTest(req)

	case MsgTypeCleanupTest:
		var req CleanupTestMessage
		if err := json.Unmarshal(msg.Raw, &req); err != nil {
			c.sendError(msg.RequestID, "INVALID_MESSAGE", err.Error())
			return
		}
		go c.handleCleanupTest(req)

	default:
		slog.Warn("Unknown message type", "type", msg.Type)
	}
}

func (c *Connection) handleDeploy(dm DeployMessage) {
	slog.Info("Deploying integration", "integration_id", dm.IntegrationID, "revision", dm.Revision, "routes", len(dm.Routes))

	result := &DeployResultMessage{
		Type:          MsgTypeDeployResult,
		RequestID:     dm.RequestID,
		IntegrationID: dm.IntegrationID,
		Revision:      dm.Revision,
	}

	// Convert cloud.SecretEntry → deploy.SecretEntry for the deployer.
	deploySecrets := make([]deploy.SecretEntry, len(dm.Secrets))
	for i, s := range dm.Secrets {
		deploySecrets[i] = deploy.SecretEntry{Var: s.Var, Cipher: s.Cipher, Value: s.Value}
	}

	// Write kamelet files to /data/kamelets/ (if provided)
	if len(dm.KameletFiles) > 0 {
		kameletDir := filepath.Join(c.identity.DataDir, "kamelets")
		os.MkdirAll(kameletDir, 0755)
		for name, content := range dm.KameletFiles {
			path := filepath.Join(kameletDir, name)
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				slog.Warn("Failed to write kamelet file", "name", name, "error", err)
			} else {
				slog.Info("Kamelet file written", "name", name)
			}
		}
	}

	// Deploy routes — deployer handles decryption and .properties file writing.
	routeStatuses := make([]RouteStatus, 0, len(dm.Routes))
	var deployErr error
	var allWarnings []string
	for _, route := range dm.Routes {
		warnings, err := c.deployer.Deploy(route.ID, route.CamelYAML, dm.Properties, deploySecrets)
		allWarnings = append(allWarnings, warnings...)
		if err != nil {
			deployErr = err
			routeStatuses = append(routeStatuses, RouteStatus{ID: route.ID, Status: "Failed"})
		} else {
			routeStatuses = append(routeStatuses, RouteStatus{ID: route.ID, Status: "Deployed"})
		}
	}
	if len(allWarnings) > 0 {
		result.Warnings = allWarnings
	}

	result.Routes = routeStatuses
	if deployErr != nil {
		result.Status = "error"
		result.Error = deployErr.Error()
	} else {
		result.Status = "success"

		// Wait for Camel to pick up routes, then verify health
		go func() {
			time.Sleep(3 * time.Second)
			healthStatuses := c.camel.CheckRoutes(routeIDs(dm.Routes))
			for i, rs := range routeStatuses {
				if hs, ok := healthStatuses[rs.ID]; ok {
					routeStatuses[i].Status = hs
				}
			}
			// Send updated status
			result.Routes = routeStatuses
			c.sendMessage(result)
		}()
	}

	c.sendMessage(result)
}

// SendRouteError satisfies monitor.Sender — pushes error event to cloud.
func (c *Connection) SendRouteError(evt monitor.RouteErrorEvent) {
	c.sendMessage(&RouteErrorMessage{
		Type:          MsgTypeRouteError,
		EngineID:      c.identity.EngineID,
		IntegrationID: evt.IntegrationID,
		FailureDelta:  evt.FailureDelta,
		TotalFailures: evt.TotalFailures,
		InFlight:      evt.InFlight,
		Timestamp:     evt.Timestamp,
	})
}

// ListDeployedRoutes satisfies monitor.Sender — returns currently deployed route IDs.
func (c *Connection) ListDeployedRoutes() []string {
	routes, _ := c.deployer.ListDeployed()
	return routes
}

// UpdateRunStats satisfies monitor.Sender — updates message count for the active run.
func (c *Connection) UpdateRunStats(integrationID string, totalExchanges int64) {
	c.runsMu.Lock()
	rs, ok := c.currentRuns[integrationID]
	if ok {
		rs.exchanges = totalExchanges
	}
	c.runsMu.Unlock()

	if !ok || c.getRunStore() == nil {
		return
	}
	if err := c.getRunStore().UpdateStats(rs.runID, totalExchanges); err != nil {
		slog.Warn("Failed to update run stats", "error", err)
	}
}

// RecordExchangeBatch satisfies monitor.Sender — records a completed run for each polling batch with new exchanges.
func (c *Connection) RecordExchangeBatch(integrationID string, count int64, failures int64) {
	store := c.getRunStore()
	if store == nil {
		return
	}

	now := time.Now().UTC()
	runID := runlog.GenerateRunID()
	status := runlog.StatusCompleted
	if failures > 0 {
		status = runlog.StatusFailed
	}

	run := runlog.Run{
		RunID:         runID,
		IntegrationID: integrationID,
		EngineID:      c.identity.EngineID,
		Status:        status,
		StartedAt:     now.Format(time.RFC3339),
		FinishedAt:    now.Format(time.RFC3339),
		DurationMs:    0,
		MessageCount:  count,
	}
	if failures > 0 {
		run.ErrorSummary = fmt.Sprintf("%.0f exchange failure(s)", float64(failures))
	}

	if err := store.StartRun(run); err != nil {
		slog.Warn("RecordExchangeBatch: StartRun failed", "err", err)
		return
	}
	if status == runlog.StatusCompleted {
		if err := store.CompleteRun(runID, count, 0); err != nil {
			slog.Warn("RecordExchangeBatch: CompleteRun failed", "err", err)
		}
	} else {
		if err := store.FailRun(runID, run.ErrorSummary, ""); err != nil {
			slog.Warn("RecordExchangeBatch: FailRun failed", "err", err)
		}
	}

	// Send latest completed run to cloud
	runs, _, err := store.QueryRuns(integrationID, runlog.StatusCompleted, 1, 0)
	if err == nil && len(runs) > 0 {
		c.sendMessage(&RunEventMessage{Type: MsgTypeRunEvent, Run: runs[0]})
	}
}

// RecordRunFailure satisfies monitor.Sender — marks the active run as failed.
func (c *Connection) RecordRunFailure(integrationID string, errorSummary string) {
	c.runsMu.Lock()
	rs, ok := c.currentRuns[integrationID]
	if ok {
		delete(c.currentRuns, integrationID)
	}
	c.runsMu.Unlock()

	if !ok || c.getRunStore() == nil {
		return
	}
	if err := c.getRunStore().FailRun(rs.runID, errorSummary, ""); err != nil {
		slog.Warn("Failed to record run failure", "error", err)
		return
	}
	slog.Info("Run failed", "run_id", rs.runID, "integration_id", integrationID)

	// Push run event to cloud
	runs, _, err := c.getRunStore().QueryRuns(integrationID, runlog.StatusFailed, 1, 0)
	if err == nil && len(runs) > 0 {
		c.sendMessage(&RunEventMessage{Type: MsgTypeRunEvent, Run: runs[0]})
	}
}

func (c *Connection) handleGetEngineMetrics(req GetEngineMetricsMessage) {
	m, err := c.camel.GetEngineMetrics()
	result := &EngineMetrics{
		Type:      MsgTypeEngineMetrics,
		RequestID: req.RequestID,
		Available: err == nil,
	}
	if err == nil {
		result.UptimeSeconds = m["process_uptime_seconds"]
		result.CPUPercent    = m["process_cpu_usage"] * 100
		result.MemoryUsedMB  = m["jvm_memory_used_bytes"] / 1024 / 1024
		result.MemoryMaxMB   = m["jvm_memory_max_bytes"] / 1024 / 1024
	}
	c.sendMessage(result)
}

func (c *Connection) handleGetRouteMetrics(req GetRouteMetricsMessage) {
	m, err := c.camel.GetRouteMetrics(req.RouteID)
	result := &RouteMetrics{
		Type:      MsgTypeRouteMetrics,
		RequestID: req.RequestID,
		RouteID:   req.RouteID,
		Available: err == nil,
	}
	if err == nil {
		result.ExchangesTotal    = m["camel_exchanges_total"]
		result.ExchangesFailed   = m["camel_exchanges_failed_total"]
		result.ExchangesInflight = m["camel_exchanges_inflight"]
		result.MeanDurationMs    = m["camel_exchange_duration_milliseconds_sum"] /
			max(m["camel_exchange_duration_milliseconds_count"], 1)
		result.MaxDurationMs = m["camel_exchange_duration_milliseconds_max"]
	}
	c.sendMessage(result)
}

func (c *Connection) handleGetHTTPPaths(req GetHTTPPathsMessage) {
	paths, err := c.camel.GetPlatformHTTPPaths()
	result := &HTTPPathsResponse{
		Type:      MsgTypeHTTPPaths,
		RequestID: req.RequestID,
		Available: err == nil,
		Paths:     []HTTPPathEntry{},
	}
	if err == nil {
		for _, p := range paths {
			result.Paths = append(result.Paths, HTTPPathEntry{
				Path:          p.Path,
				Methods:       p.Methods,
				IntegrationID: p.IntegrationID,
			})
		}
	}
	c.sendMessage(result)
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (c *Connection) handleCheckVars(msg CheckVarsMessage) {
	present := make([]string, 0)
	missing := make([]string, 0)
	for _, varName := range msg.Vars {
		if _, ok := c.deployer.HasVar(varName); ok {
			present = append(present, varName)
		} else {
			missing = append(missing, varName)
		}
	}
	c.sendMessage(&VarsResultMessage{
		Type:      MsgTypeVarsResult,
		RequestID: msg.RequestID,
		Present:   present,
		Missing:   missing,
	})
	slog.Info("Check vars", "present", len(present), "missing", len(missing))
}

func (c *Connection) handleRouteCommand(msgType string, cmd RouteCommandMessage) {
	result := &RouteResultMessage{
		Type:      MsgTypeRouteResult,
		RequestID: cmd.RequestID,
		RouteID:   cmd.RouteID,
	}

	var err error
	switch msgType {
	case MsgTypeSuspendRoute:
		err = c.camel.SuspendRoute(cmd.RouteID)
		if err == nil {
			result.Status = "Suspended"
		}
	case MsgTypeResumeRoute:
		err = c.camel.ResumeRoute(cmd.RouteID)
		if err == nil {
			result.Status = "Started"
		}
	case MsgTypeRouteStatus:
		result.Status, err = c.camel.RouteStatus(cmd.RouteID)
	}

	if err != nil {
		if err == camel.ErrNoSidecar {
			// No Camel sidecar — report desired state so cloud can track it in DB
			slog.Warn("No Camel sidecar — reporting desired state", "type", msgType, "route", cmd.RouteID)
			// result.Status already set above; treat as success so cloud updates DB
		} else {
			result.Status = "error"
			result.Error = err.Error()
			slog.Error("Route command failed", "type", msgType, "route", cmd.RouteID, "error", err)
		}
	} else {
		slog.Info("Route command ok", "type", msgType, "route", cmd.RouteID, "status", result.Status)
	}

	c.sendMessage(result)
}

func (c *Connection) handleUndeploy(um UndeployMessage) {
	slog.Info("Undeploying integration", "integration_id", um.IntegrationID)

	if err := c.deployer.Undeploy(um.IntegrationID); err != nil {
		c.sendError(um.RequestID, "UNDEPLOY_FAILED", err.Error())
		return
	}

	c.sendMessage(&DeployResultMessage{
		Type:          MsgTypeDeployResult,
		RequestID:     um.RequestID,
		IntegrationID: um.IntegrationID,
		Status:        "success",
	})
}


// getRunStore lazily initializes the run store on first use.
// This avoids failures at startup when the /data PVC may not be ready yet.
func (c *Connection) getRunStore() *runlog.Store {
	c.runStoreOnce.Do(func() {
		rs, err := runlog.NewStore(c.identity.DataDir)
		if err != nil {
			slog.Warn("Failed to init run store", "error", err)
			return
		}
		c.runStoreInst = rs
	})
	return c.runStoreInst
}

func (c *Connection) handleGetRunHistory(req GetRunHistoryMessage) {
	if c.getRunStore() == nil {
		c.sendError(req.RequestID, "RUN_STORE_UNAVAILABLE", "run history store is not initialized")
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	runs, total, err := c.getRunStore().QueryRuns(req.IntegrationID, req.Status, limit, req.Offset)
	if err != nil {
		c.sendError(req.RequestID, "QUERY_FAILED", err.Error())
		return
	}
	if runs == nil {
		runs = []runlog.Run{}
	}
	c.sendMessage(&RunHistoryMessage{
		Type:          MsgTypeRunHistory,
		RequestID:     req.RequestID,
		IntegrationID: req.IntegrationID,
		Runs:          runs,
		Total:         total,
		Offset:        req.Offset,
	})
}

func (c *Connection) handleTelemetry(requestID string) {
	healthData := c.camel.GetHealth()
	sysMetrics := system.GetSystemMetrics()

	// Convert health types to cloud message types
	routes := make([]RouteStatus, len(healthData.Routes))
	for i, r := range healthData.Routes {
		routes[i] = RouteStatus{ID: r.ID, Status: r.Status}
	}

	c.sendMessage(&TelemetryResponse{
		Type:      MsgTypeHealth,
		EngineID:  c.identity.EngineID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Health:    healthData.Status,
		Routes:    routes,
		System: SystemMetrics{
			CPUPercent:    sysMetrics.CPUPercent,
			MemoryMB:      sysMetrics.MemoryMB,
			UptimeSeconds: sysMetrics.UptimeSeconds,
		},
	})
}

func (c *Connection) sendMessage(msg interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ws == nil {
		return
	}
	c.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.ws.WriteJSON(msg); err != nil {
		slog.Warn("Failed to send message", "error", err)
	}
}

func (c *Connection) sendError(requestID, code, message string) {
	c.sendMessage(&ErrorMessage{
		Type:      MsgTypeError,
		RequestID: requestID,
		Code:      code,
		Message:   message,
	})
}

func routeIDs(routes []RouteBundle) []string {
	ids := make([]string, len(routes))
	for i, r := range routes {
		ids[i] = r.ID
	}
	return ids
}

func (c *Connection) decryptSecret(cipherBase64 string) (string, error) {
	if c.privKey == nil {
		return "", fmt.Errorf("private key not loaded")
	}
	return pairing.DecryptSecret(cipherBase64, c.privKey)
}

// ─── Sandbox test handlers ──────────────────────────────────────────────

func (c *Connection) handleDeployTest(req DeployTestMessage) {
	start := time.Now()
	nonce := req.Nonce
	routeID := req.RouteID
	timeout := req.CaptureTimeout
	if timeout <= 0 {
		timeout = 15
	}

	slog.Info("Sandbox: deploying test route", "nonce", nonce, "route_id", routeID)

	// 1. Write test files
	for path, content := range req.TestFiles {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			c.sendTestResult(req.RequestID, nonce, false, nil, "", time.Since(start).Milliseconds(), fmt.Sprintf("mkdir %s: %v", dir, err))
			return
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			c.sendTestResult(req.RequestID, nonce, false, nil, "", time.Since(start).Milliseconds(), fmt.Sprintf("write %s: %v", path, err))
			return
		}
		slog.Info("Sandbox: wrote test file", "path", path, "bytes", len(content))
	}

	// 2. Write kamelet files (if provided)
	if len(req.KameletFiles) > 0 {
		kameletDir := filepath.Join(c.identity.DataDir, "kamelets")
		os.MkdirAll(kameletDir, 0755)
		for name, content := range req.KameletFiles {
			path := filepath.Join(kameletDir, name)
			os.WriteFile(path, []byte(content), 0644)
			slog.Info("Sandbox: wrote kamelet file", "name", name)
		}
	}

	// 3. Deploy route (no properties or secrets for sandbox — values pre-substituted)
	_, err := c.deployer.Deploy(routeID, req.CamelYAML, nil, nil)
	if err != nil {
		c.sendTestResult(req.RequestID, nonce, false, nil, "", time.Since(start).Milliseconds(), fmt.Sprintf("deploy: %v", err))
		return
	}

	// 4. Capture — poll ALL exchange metrics, detect new exchanges since deploy
	slog.Info("Sandbox: capturing output", "timeout_seconds", timeout)
	captures := []string{}

	// Snapshot total exchanges before test route starts
	preMetrics, _ := c.camel.ScrapeMetrics("__routes__")
	var preTotal float64
	for k, v := range preMetrics {
		if strings.Contains(k, "camel_exchanges_total") && !strings.Contains(k, "{") {
			preTotal = v
		}
	}

	// Wait for hot-reload to pick up the route (up to 10s)
	time.Sleep(3 * time.Second)

	deadline := time.After(time.Duration(timeout-3) * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var postTotal float64
	stableCount := 0

	for {
		select {
		case <-deadline:
			goto done
		case <-ticker.C:
			curMetrics, err := c.camel.ScrapeMetrics("__routes__")
			if err != nil {
				continue
			}
			var curTotal float64
			for k, v := range curMetrics {
				if strings.Contains(k, "camel_exchanges_total") && !strings.Contains(k, "{") {
					curTotal = v
				}
			}
			delta := curTotal - preTotal
			if delta > 0 && curTotal == postTotal {
				stableCount++
				if stableCount >= 2 {
					goto done
				}
			} else {
				stableCount = 0
			}
			postTotal = curTotal
		}
	}

done:
	exchangeDelta := postTotal - preTotal
	exchangeCount := int(exchangeDelta)

	// Also check per-route failures
	var failCount int
	finalMetrics, _ := c.camel.ScrapeMetrics("__routes__")
	for k, v := range finalMetrics {
		if strings.Contains(k, "camel_exchanges_failed_total") && !strings.Contains(k, "{") {
			failCount = int(v) // total failures (not delta — good enough for sandbox)
		}
	}

	success := exchangeCount > 0 && failCount == 0
	logSummary := fmt.Sprintf("exchanges: %d total, %d failed", exchangeCount, failCount)

	if exchangeCount > 0 {
		captures = append(captures, fmt.Sprintf("%d exchange(s) completed successfully", exchangeCount))
	}
	if failCount > 0 {
		captures = append(captures, fmt.Sprintf("%d exchange(s) failed", failCount))
	}

	// 5. Undeploy test route
	c.deployer.Undeploy(routeID)
	slog.Info("Sandbox: test complete", "nonce", nonce, "success", success, "exchanges", exchangeCount, "duration_ms", time.Since(start).Milliseconds())

	c.sendTestResult(req.RequestID, nonce, success, captures, logSummary, time.Since(start).Milliseconds(), "")
}

func (c *Connection) handleCleanupTest(req CleanupTestMessage) {
	nonce := req.Nonce
	routeID := req.RouteID

	slog.Info("Sandbox: cleaning up test", "nonce", nonce, "route_id", routeID)

	// Undeploy route (idempotent)
	c.deployer.Undeploy(routeID)

	// Clean up temp files
	sandboxDir := fmt.Sprintf("/tmp/sandbox/%s", nonce)
	os.RemoveAll(sandboxDir)

	slog.Info("Sandbox: cleanup complete", "nonce", nonce)
}

func (c *Connection) sendTestResult(requestID, nonce string, success bool, captures []string, logs string, durationMs int64, errMsg string) {
	if captures == nil {
		captures = []string{}
	}
	c.sendMessage(&TestResultMessage{
		Type:       MsgTypeTestResult,
		RequestID:  requestID,
		Nonce:      nonce,
		Success:    success,
		Captures:   captures,
		Logs:       logs,
		DurationMs: durationMs,
		Error:      errMsg,
	})
}
