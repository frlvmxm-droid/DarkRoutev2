package config

import "time"

// Protocol identifies the VPN tunnel type.
type Protocol string

const (
	ProtocolWireGuard Protocol = "wg"
	ProtocolAmneziaWG Protocol = "awg"
	ProtocolVLESS     Protocol = "vless"
)

// DPISettings controls per-tunnel DPI auto-tuning behaviour.
type DPISettings struct {
	// AutoTune enables automatic generation and testing of DPI-evasion
	// parameter variants for this tunnel. Default: true.
	AutoTune bool `json:"auto_tune"`
}

// TunnelConfig is a single VPN configuration entry stored in
// /etc/vpn-watchdog/configs/<id>.json.
type TunnelConfig struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Protocol       Protocol `json:"protocol"`
	Enabled        bool     `json:"enabled"`
	InterfaceName  string   `json:"interface_name"`
	RoutingTableID int      `json:"routing_table_id"`
	MTU            int      `json:"mtu,omitempty"`

	WireGuard *WireGuardConfig `json:"wg,omitempty"`
	AmneziaWG *AmneziaWGConfig `json:"awg,omitempty"`
	VLESS     *VLESSConfig     `json:"vless,omitempty"`

	// DPI holds optional per-tunnel DPI auto-tune settings.
	DPI *DPISettings `json:"dpi,omitempty"`

	// IsVariant is true for auto-generated DPI variants; they are never
	// persisted to /etc/vpn-watchdog/configs/.
	IsVariant bool `json:"-"`
	// BaseConfigID is the ID of the original config this variant was derived from.
	BaseConfigID string `json:"-"`
}

// WireGuardConfig holds WireGuard-specific parameters.
type WireGuardConfig struct {
	PrivateKey          string   `json:"private_key"`
	PublicKey           string   `json:"public_key"`
	PresharedKey        string   `json:"preshared_key,omitempty"`
	Endpoint            string   `json:"endpoint"`
	AllowedIPs          []string `json:"allowed_ips"`
	PersistentKeepalive int      `json:"persistent_keepalive,omitempty"`
	DNS                 []string `json:"dns,omitempty"`
}

// AmneziaWGConfig extends WireGuard with Amnezia obfuscation fields.
type AmneziaWGConfig struct {
	WireGuardConfig

	// Amnezia-specific obfuscation parameters.
	JunkPacketCount            int `json:"junk_packet_count"`
	JunkPacketMinSize          int `json:"junk_packet_min_size"`
	JunkPacketMaxSize          int `json:"junk_packet_max_size"`
	InitPacketJunkSize         int `json:"init_packet_junk_size"`
	ResponsePacketJunkSize     int `json:"response_packet_junk_size"`
	InitPacketMagicHeader      int `json:"init_packet_magic_header"`
	ResponsePacketMagicHeader  int `json:"response_packet_magic_header"`
	UnderLoadPacketMagicHeader int `json:"under_load_packet_magic_header"`
	TransportPacketMagicHeader int `json:"transport_packet_magic_header"`
}

// VLESSConfig holds VLESS/Xray/sing-box parameters.
type VLESSConfig struct {
	UUID             string `json:"uuid"`
	Address          string `json:"address"`
	Port             int    `json:"port"`
	Security         string `json:"security"`       // tls | reality | none
	Flow             string `json:"flow,omitempty"` // xtls-rprx-vision
	Transport        string `json:"transport"`      // tcp | ws | grpc | httpupgrade
	Fingerprint      string `json:"fingerprint,omitempty"`
	SNI              string `json:"sni,omitempty"`
	RealityPublicKey string `json:"reality_public_key,omitempty"`
	RealityShortID   string `json:"reality_short_id,omitempty"`
	ECH              bool   `json:"ech,omitempty"`
	ECHConfig        string `json:"ech_config,omitempty"`
	DomainFronting   bool   `json:"domain_fronting,omitempty"`
	FrontingHost     string `json:"fronting_host,omitempty"`
	ClientHelloSplit bool   `json:"client_hello_split,omitempty"`
	ClientHelloBytes int    `json:"client_hello_bytes,omitempty"`
	FakeHelloTTL     int    `json:"fake_hello_ttl,omitempty"`
	ShadowTLS        bool   `json:"shadowtls,omitempty"`
	ShadowTLSServer  string `json:"shadowtls_server,omitempty"`
	Cloak            bool   `json:"cloak,omitempty"`
	CloakServer      string `json:"cloak_server,omitempty"`
	// WebSocket path, gRPC service name, etc.
	TransportPath string `json:"transport_path,omitempty"`
	// Local SOCKS5 port that sing-box/xray listens on, traffic is routed here.
	LocalPort int `json:"local_port,omitempty"`
}

// ProbeTarget is a connectivity check destination.
type ProbeTarget struct {
	Host    string    `json:"host"`
	Port    int       `json:"port,omitempty"`
	Type    ProbeType `json:"type"` // icmp | tcp | http | https
	Timeout time.Duration
}

type ProbeType string

const (
	ProbeICMP  ProbeType = "icmp"
	ProbeTCP   ProbeType = "tcp"
	ProbeHTTP  ProbeType = "http"
	ProbeHTTPS ProbeType = "https"
)

// AIAdvisorConfig controls optional external LLM-powered diagnostics.
type AIAdvisorConfig struct {
	Enabled         bool
	Provider        string // disabled | http_json
	Endpoint        string
	APIKeyFile      string
	Timeout         time.Duration
	MaxCallsPerHour int
	MinConfidence   float64
	PresetTTL       time.Duration
}

// DaemonConfig holds runtime settings, loaded from UCI / config file.
type DaemonConfig struct {
	// Probe intervals.
	ProbeIntervalHealthy  time.Duration
	ProbeIntervalDegraded time.Duration

	// Failure thresholds.
	DegradedThreshold int // consecutive failures to enter DEGRADED
	ProbingThreshold  int // additional failures to enter PROBING

	// Timeouts.
	ProbeTimeout         time.Duration
	SwitchVerifyTimeout  time.Duration
	ParallelProbeTimeout time.Duration

	// Behaviour.
	MaxSwitchAttempts  int
	PostSwitchCooldown time.Duration

	// Targets to probe.
	ProbeTargets       []ProbeTarget
	ProbeRotateTargets bool // Dynamically rotate target subset on each cycle.
	ProbeTargetPool    int  // Max number of targets sampled from ProbeTargets.
	ProbeUseDoH        bool // Resolve probe hostnames via DoH where possible.
	ProbeDoHEndpoint   string
	// User-defined VPN bypass selectors (for policy/routing integration).
	VPNDomains     []string
	VPNIPs         []string
	VPNDomainFiles []string

	// Directory containing TunnelConfig JSON files.
	ConfigDir string
	// Directory for runtime state (tmpfs).
	StateDir string
	// Path to sing-box binary.
	SingBoxBin string
	// Path to awg/wg binaries.
	WGBin  string
	AWGBin string
	// Log file path (in addition to syslog).
	LogFile string

	// DPI auto-tuning settings.
	DPIAutoTune    bool   // Enable DPI variant generation globally (default: true)
	DPIMaxVariants int    // Max variants per base config (default: 8)
	DPIProfile     string // compat | balanced | aggressive (default: balanced)

	// Optional external LLM advisor settings.
	AIAdvisor AIAdvisorConfig
}

// DefaultDaemonConfig returns sensible defaults.
func DefaultDaemonConfig() DaemonConfig {
	return DaemonConfig{
		ProbeIntervalHealthy:  30 * time.Second,
		ProbeIntervalDegraded: 10 * time.Second,
		DegradedThreshold:     3,
		ProbingThreshold:      3,
		ProbeTimeout:          5 * time.Second,
		SwitchVerifyTimeout:   60 * time.Second,
		ParallelProbeTimeout:  15 * time.Second,
		MaxSwitchAttempts:     3,
		PostSwitchCooldown:    90 * time.Second,
		ProbeTargets: []ProbeTarget{
			{Host: "1.1.1.1", Type: ProbeICMP},
			{Host: "api.telegram.org", Port: 443, Type: ProbeHTTPS},
			{Host: "connectivitycheck.gstatic.com", Port: 80, Type: ProbeHTTP},
		},
		ProbeRotateTargets: true,
		ProbeTargetPool:    3,
		ProbeUseDoH:        true,
		ProbeDoHEndpoint:   "https://1.1.1.1/dns-query",
		ConfigDir:          "/etc/vpn-watchdog/configs",
		StateDir:           "/tmp/vpn-watchdog",
		SingBoxBin:         "/usr/bin/sing-box",
		WGBin:              "/usr/bin/wg",
		AWGBin:             "/usr/bin/awg",
		DPIAutoTune:        true,
		DPIMaxVariants:     8,
		DPIProfile:         "balanced",
		AIAdvisor: AIAdvisorConfig{
			Enabled:         false,
			Provider:        "disabled",
			Timeout:         8 * time.Second,
			MaxCallsPerHour: 12,
			MinConfidence:   0.65,
			PresetTTL:       12 * time.Hour,
		},
	}
}
