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
	// Certificate is the absolute path to the PEM certificate chain; certificate renewal remains the operator's responsibility.
	Certificate string `toml:"certificate"`
	// Key is the absolute path to the certificate's PEM private key.
	Key string `toml:"key"`
}

func (n NaiveServerConfig) Enabled() bool { return n != (NaiveServerConfig{}) }
