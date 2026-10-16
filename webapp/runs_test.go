package main

import "testing"

func TestValidRunEndAndStepStatus(t *testing.T) {
	for s, want := range map[string]bool{runDone: true, runFailed: true, runInterrupted: true, runRunning: false, "": false, "Done": false} {
		if got := validRunEnd(s); got != want {
			t.Errorf("validRunEnd(%q) = %v, want %v", s, got, want)
		}
	}
	for s, want := range map[string]bool{stepActive: true, stepWaiting: true, stepDone: true, stepError: true, "": false, "running": false} {
		if got := validStepStatus(s); got != want {
			t.Errorf("validStepStatus(%q) = %v, want %v", s, got, want)
		}
	}
}
