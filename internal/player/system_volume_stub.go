//go:build !darwin

package player

import "errors"

var errSystemVolumeUnsupported = errors.New("system volume control requires macOS")

func SetSystemOutputVolume(int) error { return errSystemVolumeUnsupported }

func SetSystemMute(bool) error { return errSystemVolumeUnsupported }
