package manage

import (
	"fmt"

	"github.com/backpack/backpack/config"
	"github.com/backpack/backpack/internal/tui"
)

// The encryption a stream tunnel wraps its connections in — asked at setup
// and changeable later, in the CLI/TUI and in the panel alike. Both ends must
// carry the same; the setup link carries it to the kharej.

// encryptionOptions is the question's answers, in the order shown.
var encryptionOptions = []struct {
	value string
	opt   tui.Option
}{
	{config.EncNone, tui.Option{Title: "None", Desc: "plain wire — fastest"}},
	{config.EncStealth, tui.Option{Title: "Stealth", Desc: "encrypted, no fingerprint, silent to probes"}},
	{config.EncStealthPlus, tui.Option{Title: "Stealth+", Desc: "Stealth that also hides packet sizes and timing"}},
	{config.EncShadow, tui.Option{Title: "Shadow", Desc: "looks like TLS to a real site"}},
	{config.EncScrap, tui.Option{Title: "Scrap", Desc: "looks like plain HTTP"}},
}

// encryptionLabel names a mode for a summary line.
func encryptionLabel(mode string) string {
	for _, o := range encryptionOptions {
		if o.value == mode {
			return o.opt.Title
		}
	}
	return "None"
}

// chooseEncryption asks what wraps the tunnel's connections, among what the
// transport carries. It returns false when the operator went back. A
// transport with no choice is not asked.
func chooseEncryption(s *TunnelSpec, current string) bool {
	modes := s.EncryptionModes()
	if len(modes) <= 1 {
		return true
	}
	v, ok := askEncryption(modes, current)
	if ok {
		s.SetEncryption(v)
	}
	return ok
}

// askEncryption is the question itself, among modes, starting on current.
func askEncryption(modes []string, current string) (string, bool) {
	if current == "" {
		current = config.EncNone
	}
	var opts []tui.Option
	var values []string
	def := 0
	for _, o := range encryptionOptions {
		for _, m := range modes {
			if m == o.value {
				if o.value == current {
					def = len(values)
				}
				opts = append(opts, o.opt)
				values = append(values, o.value)
			}
		}
	}
	i := tui.ChooseOptDefault("Encryption", opts, def)
	if i < 0 {
		return "", false
	}
	return values[i], true
}

// changeEncryption is the Edit menu's entry.
func changeEncryption(name string, spec TunnelSpec) {
	fmt.Println()
	tui.Info("Now: " + encryptionLabel(spec.Encryption.EncryptionMode()) + " (The Other Side Must Match)")
	fmt.Println()
	v, ok := askEncryption(spec.EncryptionModes(), spec.Encryption.EncryptionMode())
	if !ok || v == spec.Encryption.EncryptionMode() {
		return
	}
	if err := SetEncryption(name, v); err != nil {
		tui.Error("Failed: " + err.Error())
		tui.PressEnter()
		return
	}
	tui.Success("Encryption: " + encryptionLabel(v) + " — Restarted.")
	tui.Warn("Set The Same On The Other Side.")
	tui.PressEnter()
}

// EncryptionChoice is one encryption mode as the panel's menu shows it.
type EncryptionChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

// EncryptionChoices is every mode, in the CLI's order, for the panel.
func EncryptionChoices() []EncryptionChoice {
	out := make([]EncryptionChoice, 0, len(encryptionOptions))
	for _, o := range encryptionOptions {
		out = append(out, EncryptionChoice{Value: o.value, Label: o.opt.Title, Desc: o.opt.Desc})
	}
	return out
}

// EncryptionByTransport is which modes each transport carries, for the
// panel; a transport not listed takes no encryption.
func EncryptionByTransport() map[string][]string {
	out := map[string][]string{}
	for _, tr := range []config.TransportType{config.TCP, config.TCPMUX, config.WS, config.WSS, config.WSMUX, config.WSSMUX} {
		out[string(tr)] = config.EncryptionModes(tr)
	}
	// Vision runs one inside its helper; see config.ManagedEncryptionModes.
	out["vision"] = config.ManagedEncryptionModes
	return out
}
