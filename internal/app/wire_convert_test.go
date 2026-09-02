package app

import (
	"testing"

	"github.com/rohanthewiz/cats/wire"
)

func TestDirectionMappings(t *testing.T) {
	if _, ok := SplitDirection("x"); ok {
		t.Error("bad split direction accepted")
	}
	for _, dir := range []string{wire.DirLeft, wire.DirRight, wire.DirUp, wire.DirDown} {
		if _, ok := NavDirection(dir); !ok {
			t.Errorf("NavDirection(%q) rejected", dir)
		}
	}
	if _, ok := NavDirection("northwest"); ok {
		t.Error("bad nav direction accepted")
	}
}
