//go:build !linux

package mpris

import "errors"

// Service is a no-op off Linux.
type Service struct{}

func Start(src Source, busName, artDir string) (*Service, error) {
	return nil, errors.New("MPRIS media controls are only available on Linux")
}

func (s *Service) Close() {}
