//go:build linux

package network

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type pckOwner struct {
	file *os.File
	refs int
}

// guardMu protects ownership as well as the shared rules. The lock file stays
// in place after close: unlinking it would let two processes lock different
// inodes for the same tunnel. flock releases ownership even after SIGKILL.
var pckOwners = map[string]*pckOwner{}

func lockPckTunnel(id string) (*os.File, error) {
	var ns unix.Stat_t
	if err := unix.Stat("/proc/self/ns/net", &ns); err != nil {
		return nil, err
	}
	const dir = "/run/backpack-pck"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%s.lock", ns.Ino, id))
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func acquirePckOwnership(id string) error {
	if owner := pckOwners[id]; owner != nil {
		owner.refs++
		return nil
	}
	f, err := lockPckTunnel(id)
	if err != nil {
		return fmt.Errorf("pck: cannot own tunnel firewall rules: %w", err)
	}
	// Older binaries do not take the lock. Check their packet sockets before
	// sweeping a tagged rule set that they may still be using.
	if otherPckProcess(id) {
		f.Close()
		return fmt.Errorf("pck: another process may still own this tunnel's firewall rules")
	}
	pckOwners[id] = &pckOwner{file: f, refs: 1}
	return nil
}

func releasePckOwnership(id string) {
	owner := pckOwners[id]
	if owner == nil {
		return
	}
	owner.refs--
	if owner.refs == 0 {
		owner.file.Close()
		delete(pckOwners, id)
	}
}

// CleanupPckGuards removes tagged leftovers belonging to these tokens, without
// touching any live carrier or another tunnel. It also works after a transport
// change, when no PCK carrier will be opened to perform its normal sweep.
// With no tokens, it recovers all provably unowned tagged rule sets, including
// ones left behind before a tunnel's token or configuration file was replaced.
func CleanupPckGuards(tokens ...string) {
	guardMu.Lock()
	defer guardMu.Unlock()
	ids := map[string]bool{}
	for _, token := range tokens {
		if token != "" {
			ids[pckTunnelID(token)] = true
		}
	}
	if len(tokens) == 0 {
		for _, table := range []string{"filter", "raw"} {
			out, err := pckIptables("-t", table, "-S")
			if err != nil {
				continue
			}
			for _, id := range pckRuleIDs(string(out)) {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		if pckOwners[id] != nil {
			continue
		}
		f, err := lockPckTunnel(id)
		if err != nil {
			continue
		}
		if !otherPckProcess(id) {
			sweepTunnelRules(id)
		}
		f.Close()
	}
}

// otherPckProcess covers pre-ownership binaries. A config alone is insufficient:
// it may have changed while the old engine still owns packet sockets. If such
// a Backpack process cannot be identified reliably, cleanup is withheld.
func otherPckProcess(id string) bool {
	packet, err := os.ReadFile("/proc/net/packet")
	if err != nil {
		return true
	}
	inodes := map[string]bool{}
	for _, line := range strings.Split(string(packet), "\n") {
		f := strings.Fields(line)
		if len(f) == 9 && f[0] != "sk" {
			inodes["socket:["+f[8]+"]"] = true
		}
	}
	if len(inodes) == 0 {
		return false
	}
	ns, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		return true
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, p := range processes {
		pid, err := strconv.Atoi(p.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		root := filepath.Join("/proc", p.Name())
		peerNS, err := os.Stat(root + "/ns/net")
		if err != nil || !os.SameFile(ns, peerNS) {
			continue
		}
		fds, err := os.ReadDir(root + "/fd")
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return true
			}
			continue
		}
		ownsPacket, otherOwner := false, false
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join(root, "fd", fd.Name()))
			if inodes[target] {
				ownsPacket = true
			}
			// Updated engines keep the immutable tunnel ID in their lock's
			// filename even when the on-disk config has changed. Older engines
			// have no such evidence, so their packet sockets block cleanup.
			if id != "" && strings.HasPrefix(target, "/run/backpack-pck/") && strings.HasSuffix(target, ".lock") {
				if strings.HasSuffix(target, "-"+id+".lock") {
					return true
				}
				otherOwner = true
			}
		}
		if ownsPacket && !otherOwner && pckExecutable(root) {
			return true
		}
	}
	return false
}

// Build metadata survives renaming and stripping the binary, and does not
// depend on a configuration file that may already describe the next engine.
func pckExecutable(root string) bool {
	path := root + "/exe"
	exe, _ := os.Readlink(path)
	if strings.Contains(filepath.Base(exe), "backpack") {
		return true
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return errors.Is(err, os.ErrPermission)
	}
	return info.Main.Path == "github.com/backpack/backpack" || strings.HasPrefix(info.Path, "github.com/backpack/backpack/")
}
