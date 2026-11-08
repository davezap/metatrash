package service

import (
	"fmt"

	"metatrash.com/metatrash/extension"
)

// SetListWaitObserver is a construction-only composition operation. Call before
// Handler/serving; this does not alter the waiting limit or admission policy.
func (s *Service) SetListWaitObserver(observer extension.ListWaitObserver) error {
	if s.configurationFrozen || s.listWaitObserver != nil {
		return fmt.Errorf("list wait observer must be configured once before serving")
	}
	s.listWaitObserver = observer
	return nil
}
