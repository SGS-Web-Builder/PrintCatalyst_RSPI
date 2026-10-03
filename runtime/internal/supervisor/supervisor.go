package supervisor

import (
	"context"
	"fmt"
	"sync"
)

// Component is one local subsystem managed by the On-Premise service.
type Component interface {
	Name() string
	Start(context.Context) error
	Ready(context.Context) error
	Stop(context.Context) error
}

// Supervisor owns deterministic startup, readiness and reverse-order shutdown.
type Supervisor struct {
	mu         sync.Mutex
	components []Component
	started    []Component
}

func New(components ...Component) *Supervisor {
	return &Supervisor{components: components}
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.started) != 0 {
		return nil
	}

	for _, component := range s.components {
		if err := component.Start(ctx); err != nil {
			s.stopStarted(ctx)
			return fmt.Errorf("start %s: %w", component.Name(), err)
		}
		s.started = append(s.started, component)
		if err := component.Ready(ctx); err != nil {
			s.stopStarted(ctx)
			return fmt.Errorf("ready %s: %w", component.Name(), err)
		}
	}
	return nil
}

func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopStarted(ctx)
}

func (s *Supervisor) stopStarted(ctx context.Context) error {
	var firstError error
	for index := len(s.started) - 1; index >= 0; index-- {
		component := s.started[index]
		if err := component.Stop(ctx); err != nil && firstError == nil {
			firstError = fmt.Errorf("stop %s: %w", component.Name(), err)
		}
	}
	s.started = nil
	return firstError
}
