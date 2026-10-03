package supervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeComponent struct {
	name       string
	events     *[]string
	startError error
	readyError error
}

func (f *fakeComponent) Name() string { return f.name }
func (f *fakeComponent) Start(context.Context) error {
	*f.events = append(*f.events, "start:"+f.name)
	return f.startError
}
func (f *fakeComponent) Ready(context.Context) error {
	*f.events = append(*f.events, "ready:"+f.name)
	return f.readyError
}
func (f *fakeComponent) Stop(context.Context) error {
	*f.events = append(*f.events, "stop:"+f.name)
	return nil
}

func TestSupervisorStartsInOrderAndStopsInReverse(t *testing.T) {
	events := []string{}
	s := New(
		&fakeComponent{name: "database", events: &events},
		&fakeComponent{name: "http", events: &events},
	)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:database", "ready:database", "start:http", "ready:http", "stop:http", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestSupervisorRollsBackAfterReadinessFailure(t *testing.T) {
	events := []string{}
	s := New(
		&fakeComponent{name: "database", events: &events},
		&fakeComponent{name: "http", events: &events, readyError: errors.New("not ready")},
	)
	if err := s.Start(context.Background()); err == nil {
		t.Fatal("Start() succeeded, want readiness error")
	}
	want := []string{"start:database", "ready:database", "start:http", "ready:http", "stop:http", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	events := []string{}
	s := New(&fakeComponent{name: "database", events: &events})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:database", "ready:database", "stop:database"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
