package main

import "sync"

type pageCacheFlight struct {
	wg      sync.WaitGroup
	created bool
	err     error
}

func (s *apiServer) doPageFlight(key string, fn func() (bool, error)) (bool, error, bool) {
	s.pageFlightMu.Lock()
	if s.pageFlights == nil {
		s.pageFlights = make(map[string]*pageCacheFlight)
	}
	if call, ok := s.pageFlights[key]; ok {
		s.pageFlightMu.Unlock()
		call.wg.Wait()
		return call.created, call.err, true
	}
	call := &pageCacheFlight{}
	call.wg.Add(1)
	s.pageFlights[key] = call
	s.pageFlightMu.Unlock()

	created, err := fn()
	call.created = created
	call.err = err
	call.wg.Done()

	s.pageFlightMu.Lock()
	delete(s.pageFlights, key)
	s.pageFlightMu.Unlock()
	return created, err, false
}
