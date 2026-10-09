package config

// NaiveClientConfig is the optional HTTP/2 wrapper of the existing TCP reverse
// engine. It does not own pairing, forwarding, metrics or another service.
type NaiveClientConfig struct {
	// Binary is the absolute path to the official NaiveProxy client binary; Backpack never downloads or replaces it.
	Binary string `toml:"binary"`
	// Server is the HTTP/2 proxy's host:port, reached from the outside client toward the Iran server.
	Server string `toml:"server"`
	// Username authenticates the HTTP/2 proxy independently of the reverse tunnel token.
	Username string `toml:"username"`
	// Password is the HTTP/2 proxy secret; keep the tunnel configuration readable only by its owner.
	Password string `toml:"password"`
	// CAFile optionally trusts a PEM CA for this helper alone; empty uses the official client's normal certificate verification.
	CAFile string `toml:"ca_file"`
}

func (n NaiveClientConfig) Enabled() bool { return n != (NaiveClientConfig{}) }

// NaiveServerConfig owns a compatible sing-box HTTP/2 inbound. Its generated
// routing rules permit only this tunnel's loopback control/pool target.
type NaiveServerConfig struct {
	// Binary is the absolute path to the compatible sing-box server binary; the initial integration is tested with v1.14.2.
	Binary string `toml:"binary"`
	// Listen is the public TCP host:port for HTTP/2, separate from the loopback server.bind_addr.
	Listen string `toml:"listen"`
	// Username is the proxy account accepted from the official Naive client.
	Username string `toml:"username"`
	// Password is the proxy account's secret, separate from server.token.
	Password string `toml:"password"`
	// Certificate is the absolute path to the PEM certificate chain. Renew externally; validated certificate/key replacements reload the tunnel automatically.
	Certificate string `toml:"certificate"`
	// Key is the absolute path to the certificate's PEM private key.
	Key string `toml:"key"`
}

func (n NaiveServerConfig) Enabled() bool { return n != (NaiveServerConfig{}) }

// XrayClientConfig wraps the TCP reverse connection with an official Xray
// helper. Mode selects XHTTP over verified TLS or RAW with REALITY and Vision.
type XrayClientConfig struct {
	// Binary is the absolute path to official Xray; the managed contract is tested with v26.3.27.
	Binary string `toml:"binary"`
	// Server is the outer Xray host:port; client.remote_addr remains the Iran loopback reverse listener.
	Server string `toml:"server"`
	// Mode selects xhttp (verified TLS/HTTP2) or reality (RAW, REALITY and Vision).
	Mode string `toml:"mode"`
	// UUID is one canonical UUID shared by the two helper accounts, separate from the reverse token.
	UUID string `toml:"uuid"`
	// ServerName is the explicit DNS name for TLS verification or REALITY authentication.
	ServerName string `toml:"server_name"`
	// Path is an explicit non-root private XHTTP path shared by both helpers; unused by REALITY.
	Path string `toml:"path"`
	// Host optionally overrides the XHTTP HTTP host; empty uses server_name.
	Host string `toml:"host"`
	// CAFile optionally adds a PEM trust anchor for XHTTP; normal TLS verification remains enabled.
	CAFile string `toml:"ca_file"`
	// PublicKey is the server's 32-byte unpadded base64url X25519 public key for REALITY.
	PublicKey string `toml:"public_key"`
	// ShortID is the shared 16-character hexadecimal REALITY identifier.
	ShortID string `toml:"short_id"`
}

func (x XrayClientConfig) Enabled() bool { return x != (XrayClientConfig{}) }

// XrayServerConfig accepts one VLESS account and permits only the existing
// reverse engine's loopback target. REALITY Target is an explicit TLS endpoint.
type XrayServerConfig struct {
	// Binary is the absolute path to official Xray, tested with v26.3.27.
	Binary string `toml:"binary"`
	// Listen is the public TCP host:port, separate from the loopback server.bind_addr.
	Listen string `toml:"listen"`
	// Mode selects xhttp or reality and must match the client.
	Mode string `toml:"mode"`
	// UUID is the canonical VLESS account UUID shared with the client.
	UUID string `toml:"uuid"`
	// ServerName is the DNS name in the XHTTP certificate or REALITY target's TLS identity.
	ServerName string `toml:"server_name"`
	// Path is the private XHTTP path shared with the client; unused by REALITY.
	Path string `toml:"path"`
	// Host is the optional shared XHTTP HTTP host; empty uses server_name.
	Host string `toml:"host"`
	// Certificate is the XHTTP PEM chain; externally renewed valid replacements reload automatically.
	Certificate string `toml:"certificate"`
	// Key is the XHTTP PEM private key paired with certificate.
	Key string `toml:"key"`
	// PrivateKey is the secret 32-byte unpadded base64url X25519 REALITY key.
	PrivateKey string `toml:"private_key"`
	// Target is an explicit reachable TLS1.3/H2 host:port used as the REALITY cover endpoint; choose one you control.
	Target string `toml:"target"`
	// ShortID is the shared 16-character hexadecimal REALITY identifier.
	ShortID string `toml:"short_id"`
}

func (x XrayServerConfig) Enabled() bool { return x != (XrayServerConfig{}) }
