package core

import (
	"errors"
	"fmt"
	"os"

	"github.com/backpack/backpack/internal/app"
	"github.com/backpack/backpack/internal/metrics"
	"github.com/backpack/backpack/internal/quota"
)

// Renaming a tunnel.
//
// A tunnel's name is its file's name, its unit's name, and the key everything
// kept about it is filed under: the traffic it carried, the configurations it
// had, its label, its restart schedule, its pairing with a node. A rename has
// to move all of them, or the tunnel comes back as a stranger to its own
// history. The layers above this one move what is theirs through OnRename.

var onRename []func(oldName, newName string)

// OnRename registers what a layer above has to move when a tunnel is renamed.
func OnRename(f func(oldName, newName string)) { onRename = append(onRename, f) }

// Rename gives a tunnel a new name and brings it back up under it, running if
// it was running.
func Rename(oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if err := CheckName(newName); err != nil {
		return err
	}
	if _, err := os.Stat(app.ConfigPath(newName)); err == nil {
		return fmt.Errorf("a tunnel named %q already exists", newName)
	}
	if _, err := os.Stat(app.ConfigPath(oldName)); err != nil {
		return fmt.Errorf("no tunnel named %q", oldName)
	}

	oldService, newService := app.ServiceName(oldName), app.ServiceName(newName)
	running := IsActive(oldService) || IsEnabled(oldService)
	hours, minute := ScheduledRestart(oldName)

	// Stopped first, so its last traffic count is written under the old name
	// before the file moves.
	_ = DisableService(oldService)
	removeUnit(oldName)
	removeScheduledRestart(oldName)
	restoreOld := func(why error) error {
		err := WriteUnit(oldName)
		err = errors.Join(err, DaemonReload())
		if hours > 0 {
			err = errors.Join(err, SetScheduledRestart(oldName, hours, minute))
		}
		if running {
			_, startErr := Systemctl("enable", "--now", oldService)
			err = errors.Join(err, startErr)
		}
		return errors.Join(why, err)
	}

	if err := os.Rename(app.ConfigPath(oldName), app.ConfigPath(newName)); err != nil {
		return restoreOld(fmt.Errorf("could not rename the config: %w", err))
	}
	if err := quota.Rename(app.ConfigDir, oldName, newName); err != nil {
		rollback := os.Rename(app.ConfigPath(newName), app.ConfigPath(oldName))
		return restoreOld(errors.Join(fmt.Errorf("could not rename traffic quota: %w", err), rollback))
	}
	if err := os.Rename(metrics.Path(app.ConfigDir, oldName), metrics.Path(app.ConfigDir, newName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		quotaErr := quota.Rename(app.ConfigDir, newName, oldName)
		configErr := os.Rename(app.ConfigPath(newName), app.ConfigPath(oldName))
		return restoreOld(errors.Join(fmt.Errorf("could not rename traffic counters: %w", err), quotaErr, configErr))
	}
	renameTunnelMeta(oldName, newName)
	for _, f := range onRename {
		f(oldName, newName)
	}

	if err := WriteUnit(newName); err != nil {
		return err
	}
	if err := DaemonReload(); err != nil {
		return err
	}
	if hours > 0 {
		_ = SetScheduledRestart(newName, hours, minute)
	}
	if running {
		if _, err := Systemctl("enable", "--now", newService); err != nil {
			return fmt.Errorf("renamed, but %s did not start: %w", newService, err)
		}
	}
	return nil
}

// renameTunnelMeta moves a tunnel's metadata to its new name.
func renameTunnelMeta(oldName, newName string) {
	metaMu.Lock()
	defer metaMu.Unlock()
	m := loadMeta()
	if e, ok := m[oldName]; ok {
		m[newName] = e
		delete(m, oldName)
		saveMeta(m)
	}
}
