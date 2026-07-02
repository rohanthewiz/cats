package workspace

import "testing"

func TestGeneratedWorkspaceIDsAreShortBase32Handles(t *testing.T) {
	first := GenerateWorkspaceID()
	second := GenerateWorkspaceID()

	if first[0] != 'w' {
		t.Fatalf("first id %q should start with 'w'", first)
	}
	if second[0] != 'w' {
		t.Fatalf("second id %q should start with 'w'", second)
	}
	if first == second {
		t.Fatalf("ids should be unique: %q == %q", first, second)
	}
	if len(first) > 3 {
		t.Fatalf("unexpectedly long workspace id: %s", first)
	}
	if len(second) > 3 {
		t.Fatalf("unexpectedly long workspace id: %s", second)
	}
}

func TestPublicNumbersRoundTripReadableBase32Handles(t *testing.T) {
	encodings := []struct {
		value int
		want  string
	}{
		{1, "1"},
		{9, "9"},
		{10, "A"},
		{31, "Z"},
		{32, "0"},
		{33, "11"},
	}
	for _, tc := range encodings {
		if got := EncodePublicNumber(tc.value); got != tc.want {
			t.Errorf("EncodePublicNumber(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}

	for _, value := range []int{1, 9, 10, 31, 32, 33, 1024, 1025} {
		encoded := EncodePublicNumber(value)
		decoded, ok := DecodePublicNumber(encoded)
		if !ok || decoded != value {
			t.Errorf("DecodePublicNumber(%q) = (%d, %v), want (%d, true)", encoded, decoded, ok, value)
		}
	}
}

func TestReservingRestoredWorkspaceIDsPreventsReuse(t *testing.T) {
	restored := "wZ"

	ReserveWorkspaceIDs([]string{restored})

	generated := GenerateWorkspaceID()
	if generated == restored {
		t.Fatalf("generated id %q collides with restored id", generated)
	}
	generatedNum, ok := PublicWorkspaceNumber(generated)
	if !ok {
		t.Fatalf("generated id %q should decode", generated)
	}
	restoredNum, ok := PublicWorkspaceNumber(restored)
	if !ok {
		t.Fatalf("restored id %q should decode", restored)
	}
	if generatedNum <= restoredNum {
		t.Fatalf("generated number %d should exceed restored number %d", generatedNum, restoredNum)
	}
}
