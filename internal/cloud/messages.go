package cloud

import "encoding/json"

// Message types from cloud → agent
const (
	MsgTypeDeploy        = "deploy"
	MsgTypeUndeploy      = "undeploy"
	MsgTypeSuspendRoute  = "suspend_route"
	MsgTypeResumeRoute   = "resume_route"
	MsgTypeRouteStatus   = "route_status"
	MsgTypePing          = "ping"
	MsgTypeTelemetry     = "telemetry"
	MsgTypeSetLabel      = "set_label"
)

// Message types from agent → cloud
const (
	MsgTypeConnected      = "connected"
	MsgTypeDeployResult   = "deploy_result"
	MsgTypeRouteResult    = "route_result"
	MsgTypePong           = "pong"
	MsgTypeHealth         = "telemetry"
	MsgTypeError          = "error"
)

// RouteCommandMessage is a suspend/resume/status command for a single route.
type RouteCommandMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RouteID   string `json:"route_id"`
}

// RouteResultMessage reports the result of a route command.
type RouteResultMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RouteID   string `json:"route_id"`
	Status    string `json:"status"` // Started | Suspended | Stopped | NotFound | error
	Error     string `json:"error,omitempty"`
}

// InboundMessage is a generic message from cloud.
type InboundMessage struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// DeployMessage is a deploy command from cloud.
type DeployMessage struct {
	Type          string        `json:"type"`
	RequestID     string        `json:"request_id"`
	IntegrationID string        `json:"integration_id"`
	Revision      int           `json:"revision"`
	Routes        []RouteBundle `json:"routes"`
	Secrets       []SecretEntry `json:"secrets,omitempty"`
}

type RouteBundle struct {
	ID        string `json:"id"`
	CamelYAML string `json:"camel_yaml"`
}

type SecretEntry struct {
	Var    string `json:"var"`
	Cipher string `json:"cipher,omitempty"` // base64-encoded RSA-encrypted (Phase 2)
	Value  string `json:"value,omitempty"`  // plaintext (MVP only, dev mode)
}

// UndeployMessage is an undeploy command from cloud.
type UndeployMessage struct {
	Type          string `json:"type"`
	RequestID     string `json:"request_id"`
	IntegrationID string `json:"integration_id"`
}

// ConnectedMessage is sent by agent after WSS connect.
// Uses camelCase JSON keys to match backend expectations.
type ConnectedMessage struct {
	Type           string            `json:"type"`
	EngineID       string            `json:"engineId"`
	Version        string            `json:"version"`
	Metadata       map[string]string `json:"metadata"`
	DeployedRoutes []string          `json:"deployedRoutes"` // Integration IDs currently deployed on disk
}

// DeployResultMessage reports deploy outcome.
type DeployResultMessage struct {
	Type          string        `json:"type"`
	RequestID     string        `json:"request_id"`
	IntegrationID string        `json:"integration_id"`
	Revision      int           `json:"revision"`
	Status        string        `json:"status"` // success | error
	Routes        []RouteStatus `json:"routes,omitempty"`
	Error         string        `json:"error,omitempty"`
}

type RouteStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // Started | Stopped | Failed
}

// PongMessage responds to ping.
type PongMessage struct {
	Type          string `json:"type"`
	EngineID      string `json:"engine_id"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// ErrorMessage reports errors.
type ErrorMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

// TelemetryResponse reports health data.
type TelemetryResponse struct {
	Type         string             `json:"type"`
	EngineID     string             `json:"engine_id"`
	Timestamp    string             `json:"timestamp"`
	Health       string             `json:"health"` // UP | DOWN
	Routes       []RouteStatus      `json:"routes"`
	Integrations []IntegrationState `json:"integrations"`
	System       SystemMetrics      `json:"system"`
}

type IntegrationState struct {
	ID            string `json:"id"`
	Revision      int    `json:"revision"`
	RoutesTotal   int    `json:"routes_total"`
	RoutesStarted int    `json:"routes_started"`
}

type SystemMetrics struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryMB      int64   `json:"memory_mb"`
	UptimeSeconds int64   `json:"uptime_seconds"`
}
