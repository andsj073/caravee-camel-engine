package cloud

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/caravee/engine/internal/camel"
	"github.com/caravee/engine/internal/config"
	"github.com/caravee/engine/internal/deploy"
	"github.com/caravee/engine/internal/pairing"
	"github.com/caravee/engine/internal/system"
	"github.com/gorilla/websocket"
)

const (
	maxReconnectDelay = 30 * time.Second
	pingInterval      = 30 * time.Second
	writeTimeout      = 10 * time.Second
)

// Connection manages the WSS link to Caravee Cloud.
type Connection struct {
	cfg      *config.CloudConfig
	identity *config.Identity
	deployer *deploy.Deployer
	camel   *camel.Client
	privKey  *rsa.PrivateKey // For decrypting secrets
	ws       *websocket.Conn
	mu       sync.Mutex
	done     chan struct{}
	startAt  time.Time
}

// NewConnection creates a new cloud connection.
func NewConnection(cfg *config.CloudConfig, identity *config.Identity, deployer *deploy.Deployer, camelClient *camel.Client) *Connection {
	// Load private key for secret decryption
	privKey, err := pairing.LoadPrivateKey(identity.DataDir)
	if err != nil {
		slog.Warn("Failed to load private key — secrets decryption unavailable", "error", err)
		privKey = nil
	}

	return &Connection{
		cfg:      cfg,
		identity: identity,
		deployer: deployer,
		camel:   camelClient,
		privKey:  privKey,
		done:     make(chan struct{}),
		startAt:  time.Now(),
	}
}

// Run connects and handles messages. Blocks until permanently closed.
func (c *Connection) Run() error {
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

	slog.Info("Connected to cloud")

	// Send connected message
	deployed, err := c.deployer.ListDeployed()
	if err != nil {
		slog.Warn("Could not list deployed routes", "error", err)
		deployed = []string{}
	}

	c.sendMessage(&ConnectedMessage{
		Type:     MsgTypeConnected,
		EngineID: c.identity.EngineID,
		Version:  "0.1.0",
		Metadata: map[string]string{
			"os":   "linux",
			"arch": "amd64",
		},
		DeployedRoutes: deployed,
	})
	slog.Info("Reported deployed routes", "count", len(deployed), "routes", deployed)

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

	// Build secret map — decrypt cipher if present
	secrets := make(map[string]string)
	for _, s := range dm.Secrets {
		if s.Cipher != "" {
			// Decrypt with engine private key
			plaintext, err := c.decryptSecret(s.Cipher)
			if err != nil {
				slog.Error("Failed to decrypt secret", "var", s.Var, "error", err)
				continue
			}
			secrets[s.Var] = plaintext
		} else if s.Value != "" {
			// Fallback: plaintext (dev mode)
			secrets[s.Var] = s.Value
		}
	}

	// Deploy routes
	routeStatuses := make([]RouteStatus, 0, len(dm.Routes))
	var deployErr error
	for _, route := range dm.Routes {
		if err := c.deployer.Deploy(route.ID, route.CamelYAML, secrets); err != nil {
			deployErr = err
			routeStatuses = append(routeStatuses, RouteStatus{ID: route.ID, Status: "Failed"})
		} else {
			routeStatuses = append(routeStatuses, RouteStatus{ID: route.ID, Status: "Deployed"})
		}
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
