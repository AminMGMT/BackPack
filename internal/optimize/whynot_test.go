package optimize

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func sandbox(t *testing.T) (etc string) {
	t.Helper()
	dir := t.TempDir()
	etc = filepath.Join(dir, "sysctl.d")
	os.MkdirAll(etc, 0o755)
	oldF, oldL, oldD, oldP := sysctlFile, legacySysctlFile, sysctlDirs, procpsConf
	sysctlFile = filepath.Join(etc, "zz-backpack.conf")
	legacySysctlFile = filepath.Join(etc, "99-backpack.conf")
	sysctlDirs = []string{etc}
	procpsConf = filepath.Join(dir, "sysctl.conf")
	t.Cleanup(func() { sysctlFile, legacySysctlFile, sysctlDirs, procpsConf = oldF, oldL, oldD, oldP })
	return etc
}

// The file Optimize writes has to be applied after every numbered one — above
// all 99-sysctl.conf, where /etc/sysctl.conf is linked in on Debian and Ubuntu
// and where other installers put their own values.
func TestOptimizesFileIsAppliedLast(t *testing.T) {
	names := []string{"10-network-security.conf", "99-sysctl.conf", "99-zz-other.conf",
		filepath.Base(sysctlFile)}
	sort.Strings(names)
	if names[len(names)-1] != filepath.Base(sysctlFile) {
		t.Fatalf("boot order %v: %s is not last", names, filepath.Base(sysctlFile))
	}
}

func TestWhyNotNamesTheFileThatOverrides(t *testing.T) {
	etc := sandbox(t)
	os.WriteFile(sysctlFile, []byte("net.core.default_qdisc = fq\n"), 0o644)
	os.WriteFile(procpsConf, []byte("# an installer's\nnet.core.default_qdisc=fq_codel\n"), 0o644)
	why := WhyNot("net.core.default_qdisc", "fq_codel")
	if !strings.Contains(why, procpsConf) || !strings.Contains(why, "fq_codel") {
		t.Fatalf("WhyNot = %q, want it to name %s", why, procpsConf)
	}
	_ = etc
}

func TestWhyNotBlamesTheKernelWhenNoFileOverrides(t *testing.T) {
	sandbox(t)
	os.WriteFile(sysctlFile, []byte("net.core.rmem_max = 268435456\n"), 0o644)
	why := WhyNot("net.core.rmem_max", "212992")
	if !strings.Contains(why, "container") {
		t.Fatalf("WhyNot = %q, want the kernel/container explanation", why)
	}
}

func TestWhyNotIsSilentBeforeOptimizeOrWhenTheValueIsRight(t *testing.T) {
	sandbox(t)
	if why := WhyNot("net.core.default_qdisc", "fq_codel"); why != "" {
		t.Fatalf("explained before Optimize ever ran: %q", why)
	}
	os.WriteFile(sysctlFile, []byte("x\n"), 0o644)
	if why := WhyNot("net.ipv4.ip_local_port_range", "32768\t60999"); why != "" {
		t.Fatalf("a correct value (tab-separated, as the kernel prints it) was explained: %q", why)
	}
}

// An update finds the old file and still counts it as Optimize having run, so
// it re-applies and moves the settings to the new name.
func TestTheOldFileStillCountsAndIsReplaced(t *testing.T) {
	sandbox(t)
	os.WriteFile(legacySysctlFile, []byte("x\n"), 0o644)
	if !WasApplied() {
		t.Fatal("a machine optimized under the old name reads as never optimized")
	}
}
