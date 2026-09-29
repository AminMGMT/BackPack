package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"time"
)

// LoadState reads a JSON state file into v.
//
// A missing file is not an error: every state file starts life absent. A file
// that exists but cannot be read back is the case this is for. Treating it as
// empty — which is what reading with the error ignored does — hands the caller
// a blank state, and the caller's next save writes that blank over the only
// copy of what was there: a fleet's servers, a tunnel's pairing, a hand edit
// with a stray comma that one keystroke would have fixed.
//
// Two ways a file fails, handled apart (see Unreadable):
//
//   - It does not parse. Nothing in it is usable: the file is moved aside to
//     <path>.unreadable-<unix time>, v is reset to its zero value, and the next
//     save starts a new file.
//   - It parses, but a value has the wrong type — a hand edit, or a newer or
//     older build that changed a field. Everything else was read: v keeps it,
//     and a copy of the file is kept beside it, so the field the next save
//     drops can still be recovered.
//
// Either way the error says what happened and where the copy is.
func LoadState(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) {
			if rv := reflect.ValueOf(v); rv.Kind() == reflect.Pointer && !rv.IsNil() {
				rv.Elem().SetZero()
			}
		}
		return Unreadable(path, data, err)
	}
	return nil
}

// Unreadable keeps an unreadable state file out of reach of the next save and
// says so: moved aside when it did not parse at all, copied aside when it did
// and only a value had the wrong type (the caller keeps the rest). See
// LoadState.
func Unreadable(path string, data []byte, why error) error {
	aside := fmt.Sprintf("%s.unreadable-%d", path, time.Now().Unix())
	var te *json.UnmarshalTypeError
	if errors.As(why, &te) {
		// Named by content: the file stays where it is and is read again on
		// every load, and one copy — and one warning — per version of it is
		// enough.
		sum := sha256.Sum256(data)
		aside = fmt.Sprintf("%s.unreadable-%x", path, sum[:6])
		if _, err := os.Stat(aside); err == nil {
			return nil
		}
		if err := WriteFileAtomic(aside, data, 0o600); err != nil {
			return fmt.Errorf("%s has a value of the wrong type (%v), and a copy could not be kept: %w", path, why, err)
		}
		return fmt.Errorf("%s has a value of the wrong type (%v); the rest was read, and a copy was kept as %s", path, why, aside)
	}
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("%s does not parse (%v), and could not be moved aside: %w", path, why, err)
	}
	return fmt.Errorf("%s does not parse (%v); it was kept as %s — starting from an empty one", path, why, aside)
}

// WarnState reports a state file that could not be loaded, on stderr — which
// is the journal for every service this product runs. Loading goes on either
// way; this is so the operator can learn why.
func WarnState(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "backpack: warning:", err)
	}
}
