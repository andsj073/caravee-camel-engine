package cloud

import "encoding/json"

// Message types from cloud → agent
const (
	MsgTypeDeploy        = "deploy"
	MsgTypeUndeploy      = "undeploy"
	MsgTypeSuspendRoute  = "suspend_route"
	MsgTypeResumeRoute   = "resume_route"
	MsgTypeRouteStatus   = "route_status"
	MsgTypeCheckVars       = "check_vars"
	MsgTypeGetEngineMetrics = "get_engine_metrics"
	MsgTypeGetRouteMetrics  = "get_route_metrics"
	MsgTypePing          = "ping"
	MsgTypeTelemetry     = "telemetry"
	MsgTypeSetLabel      = "set_label"
)

// Message types from agent → cloud
const (
	MsgTypeConnected      = "connected"
	MsgTypeDeployResult   = "deploy_result"
	MsgTypeRouteResult    = "route_result"
	MsgTypeVarsResult      = "vars_result"
	MsgTypeEngineMetrics   = "engine_metrics"
	MsgTypeRouteMetrics    = "route_metrics"
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
	LocalVars      []string          `json:"localVars"`      // Binding var names available locally (secrets.env + env)
}

// CheckVarsMessage asks engine to verify a list of var names.
type CheckVarsMessage struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	Vars      []string `json:"vars"` // {{varName}} refs extracted from integration spec
}

// VarsResultMessage reports which vars are present/missing.
type VarsResultMessage struct {
	Type      string   `json:"type"`
	RequestID string   `json:"request_id"`
	Present   []string `json:"present"`
	Missing   []string `json:"missing"`
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

// GetEngineMetricsMessage requests engine-level metrics (CPU, mem, uptime).
type GetEngineMetricsMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

// GetRouteMetricsMessage requests metrics for a single route.
type GetRouteMetricsMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	RouteID   string `json:"route_id"`
}

// EngineMetrics contains engine-level runtime data.
type EngineMetrics struct {
	Type          string  `json:"type"`
	RequestID     string  `json:"request_id"`
	Available     bool    `json:"available"`     // false when Camel sidecar not running
	UptimeSeconds float64 `json:"uptime_seconds"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  float64 `json:"memory_used_mb"`
	MemoryMaxMB   float64 `json:"memory_max_mb"`
}

// RouteMetrics contains per-route exchange metrics.
type RouteMetrics struct {
	Type            string  `json:"type"`
	RequestID       string  `json:"request_id"`
	RouteID         string  `json:"route_id"`
	Available       bool    `json:"available"`
	ExchangesTotal  float64 `json:"exchanges_total"`
	ExchangesFailed float64 `json:"exchanges_failed"`
	ExchangesInflight float64 `json:"exchanges_inflight"`
	MeanDurationMs  float64 `json:"mean_duration_ms"`
	MaxDurationMs   float64 `json:"max_duration_ms"`
}
