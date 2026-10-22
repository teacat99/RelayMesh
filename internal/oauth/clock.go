package oauth

import "time"

var serviceNow = time.Now

func SetServiceClock(fn func() time.Time) {
	if fn == nil {
		serviceNow = time.Now
		return
	}
	serviceNow = fn
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock()
	}
	return serviceNow()
}
