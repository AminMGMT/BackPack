package shorthttps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type FileConfig struct {
	Listen      string `json:"listen"`
	Target      string `json:"target,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Token       string `json:"token"`
	CA          string `json:"ca,omitempty"`
	Certificate string `json:"certificate,omitempty"`
	Key         string `json:"key,omitempty"`
}

// Run intentionally exposes an experimental standalone command, not an
// automatic replacement for the existing reverse/direct tunnel services.
func Run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(out, "Experimental Short HTTPS · TCP only · fixed backend · lower throughput\n  backpack short-https init --ip <server-IP> --target 127.0.0.1:<backend> --dir <private-new-directory> [--port 8443]\n  backpack short-https serve --config <directory>/server.json\n  backpack short-https client --config <directory>/client.json\nCopy only client.json to the other server. It contains the private-CA trust and shared token.\nThe client exposes the fixed remote backend at its local listen address. No existing tunnel is changed.")
		return nil
	}
	f := flag.NewFlagSet("short-https "+args[0], flag.ContinueOnError)
	f.SetOutput(out)
	path := f.String("config", "", "private JSON configuration")
	var ip, dir, target *string
	var port *int
	if args[0] == "init" {
		ip = f.String("ip", "", "server public IP")
		dir = f.String("dir", "", "new private directory")
		target = f.String("target", "", "literal loopback backend")
		port = f.Int("port", 8443, "unused public HTTPS port")
	}
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	if args[0] == "init" {
		return initialize(*ip, *target, *dir, *port, out)
	}
	if args[0] != "serve" && args[0] != "client" {
		return errors.New("choose init, serve or client")
	}
	if *path == "" {
		return errors.New("--config is required")
	}
	info, err := os.Stat(*path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("config must be a small regular JSON file")
	}
	body, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var cfg FileConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return err
	}
	if args[0] == "client" {
		c, err := NewClient(cfg.Endpoint, cfg.Token, cfg.CA)
		if err != nil {
			return err
		}
		l, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Short HTTPS · experimental\nLocal TCP: %s → %s\nFixed backend only · verified TLS · Ctrl+C stops\n", l.Addr(), cfg.Endpoint)
		return c.Serve(ctx, l, func(err error) { fmt.Fprintf(out, "Stream ended: %v\n", err) })
	}
	s, err := NewServer(ctx, cfg.Target, cfg.Token)
	if err != nil {
		return err
	}
	defer s.Close()
	pair, err := tls.LoadX509KeyPair(cfg.Certificate, cfg.Key)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return errors.New("server certificate is not currently valid")
	}
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}, CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256}}}
	stop := context.AfterFunc(ctx, func() { srv.Close() })
	defer stop()
	fmt.Fprintf(out, "Short HTTPS · experimental\nHTTPS listener: %s → fixed backend %s\nCtrl+C stops · no services installed\n", l.Addr(), cfg.Target)
	err = srv.Serve(tls.NewListener(l, srv.TLSConfig))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func initialize(address, target, dir string, port int, out io.Writer) error {
	ip := net.ParseIP(address)
	if ip == nil || dir == "" || port < 1 || port > 65535 {
		return errors.New("public IP, new directory and valid port are required")
	}
	// Validate the destination without opening a backend or listener.
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewServer(ctx, target, "0123456789abcdef0123456789abcdef")
	if err != nil {
		cancel()
		return err
	}
	s.Close()
	cancel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	now := time.Now()
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: ip.String()}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), IPAddresses: []net.IP{ip}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		return err
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(secret)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.Mkdir(abs, 0700); err != nil {
		return fmt.Errorf("use a new directory; existing state is never overwritten: %w", err)
	}
	server := FileConfig{Listen: net.JoinHostPort("0.0.0.0", fmt.Sprint(port)), Target: target, Token: token, Certificate: filepath.Join(abs, "server.crt"), Key: filepath.Join(abs, "server.key")}
	client := FileConfig{Listen: "127.0.0.1:0", Endpoint: "https://" + net.JoinHostPort(ip.String(), fmt.Sprint(port)), Token: token, CA: string(cert)}
	serverBody, _ := json.MarshalIndent(server, "", "  ")
	clientBody, _ := json.MarshalIndent(client, "", "  ")
	for name, data := range map[string][]byte{"server.crt": cert, "server.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), "server.json": serverBody, "client.json": clientBody} {
		if err := os.WriteFile(filepath.Join(abs, name), data, 0600); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Created experimental paired settings in %s\nCopy only client.json to the other server; set its listen to an unused local TCP port.\nPrivate IP certificate expires %s. No service or tunnel was started.\n", abs, leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}
