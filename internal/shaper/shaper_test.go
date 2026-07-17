package shaper

import (
	"context"
	"errors"
	"strings"
	"testing"

	"streamwreck/internal/run"
)

func TestCheckDevice_OK(t *testing.T) {
	fake := run.NewFake()
	fake.Outputs["qdisc show dev eth0"] = "qdisc noqueue 0: root"
	s := New(fake, "shaper", Dev)
	if err := s.CheckDevice(context.Background()); err != nil {
		t.Fatalf("expected device visible, got %v", err)
	}
}

func TestCheckDevice_StaleNetnsGivesActionableError(t *testing.T) {
	fake := run.NewFake()
	fake.Errors["qdisc show dev eth0"] = errors.New("Cannot find device \"eth0\"")
	s := New(fake, "shaper", Dev)
	err := s.CheckDevice(context.Background())
	if err == nil {
		t.Fatal("expected an error when the shaper cannot see its device")
	}
	// The message must point the user at the fix, not just echo the tc error.
	if !strings.Contains(err.Error(), "down && streamwreck up") {
		t.Errorf("error should tell the user to restart the stack, got: %v", err)
	}
}
