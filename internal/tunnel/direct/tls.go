package direct

import (
	"crypto/tls"
	"fmt"
	"net"

	"github.com/backpack/backpack/internal/utils/network"
	"github.com/sirupsen/logrus"
)

// The origin's certificate for wss.
//
// Three ways to get one, in order of what the operator asked for:
//
//	acme_domain          Let's Encrypt, renewed automatically
//	tls_cert + tls_key   a certificate the operator supplies
//	neither              one generated here, in memory
//
// The third is the default because it is what a direct connection to an IP
// address wants, and it is safe for the same reason the whole design is: the
// tunnel's trust anchor is the token, proved inside the connection, over every
// transport alike. TLS here is transport and camouflage. An edge that has been
// told a `server_name` verifies the certificate against it and needs one of
// the first two; an edge that has not is talking to an address rather than a
// name, where no certificate could mean anything anyway.
//
// The generated certificate lives only in memory and a restart makes a new
// one. Nothing depends on it being stable — the edge is not pinning it — and
// keeping it out of the filesystem means one less secret on disk.

// originTLSConfig builds the server-side TLS configuration for wss.
func originTLSConfig(cfg *Config, log *logrus.Logger) (*tls.Config, error) {
	if cfg.ACMEDomain != "" || cfg.TLSCertFile != "" {
		tlsConfig, err := network.ServerTLSConfig(network.TLSSettings{
			CertFile:     cfg.TLSCertFile,
			KeyFile:      cfg.TLSKeyFile,
			ACMEDomain:   cfg.ACMEDomain,
			ACMEEmail:    cfg.ACMEEmail,
			ACMECacheDir: cfg.ACMECacheDir,
		}, log.Infof)
		if err != nil {
			return nil, fmt.Errorf("direct: preparing TLS: %w", err)
		}
		return tlsConfig, nil
	}

	cert, err := network.GenerateSelfSigned(cfg.hostForCert())
	if err != nil {
		return nil, fmt.Errorf("direct: generating a certificate: %w", err)
	}
	log.Infof("direct: serving wss with a generated certificate (the token is what authenticates the tunnel)")
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		// Pinned so a client that offers HTTP/2 cannot negotiate the upgrade
		// away: a websocket handshake is an HTTP/1.1 thing.
		NextProtos: []string{"http/1.1"},
	}, nil
}

// hostForCert is the name or address to put in the generated certificate. It
// is cosmetic — nothing verifies it by default — but a certificate that names
// the host it is served from is what an inspecting eye expects to see.
func (c *Config) hostForCert() string {
	if c.ServerName != "" {
		return c.ServerName
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return "localhost"
	}
	return host
}
