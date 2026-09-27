package platform

import "errors"

// CheckPermission has nothing to check on this OS.
func CheckPermission() (Permission, error) { return Permission{Access: AccessNotNeeded}, nil }

// RequestPermission has nothing to ask on this OS.
func RequestPermission() (Access, error) { return AccessNotNeeded, nil }

// ConsoleState is macOS only.
func ConsoleState() (Console, error) { return Console{}, errors.ErrUnsupported }

// HIDClients is macOS only.
func HIDClients() ([]DeviceClients, error) { return nil, errors.ErrUnsupported }
