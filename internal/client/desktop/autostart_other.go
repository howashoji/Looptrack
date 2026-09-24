//go:build !windows

package desktop

import "github.com/howashoji/looptrack/internal/i18n"

var errNoRegistry = i18n.Errorf("desktop.err.registry_windows_only")

func regGetRun() (string, error) { return "", errNoRegistry }
func regSetRun(string) error     { return errNoRegistry }
func regDeleteRun() error        { return errNoRegistry }
