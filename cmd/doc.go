// Package cmd is engine mode: one tunnel, run from one configuration file
// (`backpack -c <file>`), which is what every tunnel's systemd unit executes.
//
// It loads the file, fills in the defaults (applyDefaults), refuses what cannot
// run (validateConfig — the same answer `backpack check` gives; see
// CheckConfigFile), and hands the configuration to the engine the file names:
// the layer-3 tunnel for [l3], the direct tunnel for [direct], the reverse
// tunnel for [server]/[client]. It then watches the file and restarts the
// tunnel in place when it changes (reload.go), keeping the running tunnel when
// the new file is unusable.
package cmd
