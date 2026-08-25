package main

import "testing"

func TestStubConfigNonEmpty(t *testing.T) {
	a := NewApp()
	hosts := a.ConfigStub()
	if len(hosts) == 0 {
		t.Fatal("expected at least a placeholder host")
	}
}

func TestStubToggleErrors(t *testing.T) {
	a := NewApp()
	if err := a.ToggleTunnelStub("x"); err == nil {
		t.Fatal("expected ToggleTunnelStub to error in stub phase")
	}
}
