package instanceid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Identity is persistent ownership metadata shared by the runtime and the
// management plane. Connmark is stable because existing conntrack entries must
// keep working while a new rule generation replaces the old one.
type Identity struct {
	InstanceID string `json:"instance_id"`
	Connmark   uint32 `json:"connmark"`
}

func Name(configPath string) string {
	base := filepath.Base(configPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func Dir(configPath string) string  { return filepath.Join(filepath.Dir(configPath), "instances") }
func Path(configPath string) string { return filepath.Join(Dir(configPath), Name(configPath)+".json") }

// Deterministic derives a UUID-shaped stable identity from the canonical path.
// This gives legacy and hand-written configs an identity without rewriting the
// TOML or requiring an operator migration.
func Deterministic(configPath string) Identity {
	abs, err := filepath.Abs(configPath)
	if err != nil {
		abs = filepath.Clean(configPath)
	}
	sum := sha256.Sum256([]byte("backpack-instance-v1:" + filepath.ToSlash(abs)))
	b := append([]byte(nil), sum[:16]...)
	b[6] = (b[6] & 0x0f) | 0x50 // UUID v5-shaped
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
	mark := uint32(sum[16])<<24 | uint32(sum[17])<<16 | uint32(sum[18])<<8 | uint32(sum[19])
	mark &= 0x7fffffff
	if mark == 0 {
		mark = 1
	}
	return Identity{InstanceID: id, Connmark: mark}
}

func Load(configPath string) (Identity, error) {
	b, err := os.ReadFile(Path(configPath))
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, err
	}
	if id.InstanceID == "" || id.Connmark == 0 {
		return Identity{}, fmt.Errorf("identity metadata is incomplete")
	}
	return id, nil
}

// Resolve returns an existing identity or the deterministic legacy identity.
// Persistence happens only when requested by Run/management, never Validate.
func Resolve(configPath string, persist bool) (Identity, error) {
	if id, err := Load(configPath); err == nil {
		return id, nil
	}
	id := Deterministic(configPath)
	if !persist {
		return id, nil
	}
	if err := persistIdentity(configPath, id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func persistIdentity(configPath string, id Identity) error {
	if err := os.MkdirAll(Dir(configPath), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(id, "", "  ")
	tmp := Path(configPath) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, Path(configPath)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Create allocates a random identity for a newly managed instance. Hand-written
// and legacy configs still use Resolve's deterministic path-derived fallback,
// so they need no migration.
func Create(configPath string) (Identity, error) {
	if id, err := Load(configPath); err == nil {
		return id, nil
	}
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return Identity{}, err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := Identity{
		InstanceID: fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16])),
		Connmark:   binary.BigEndian.Uint32(b[16:20]) & 0x7fffffff,
	}
	if id.Connmark == 0 {
		id.Connmark = 1
	}
	if err := persistIdentity(configPath, id); err != nil {
		return Identity{}, err
	}
	return id, nil
}

func Hash80(id string) string {
	s := sha256.Sum256([]byte(id))
	return hex.EncodeToString(s[:10])
}
