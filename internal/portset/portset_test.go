package portset

import "testing"

func TestParseAndHas(t *testing.T) {
	s, err := Parse([]string{"3000", "5173-5183", " 8080 "})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, p := range []int{3000, 5173, 5180, 5183, 8080} {
		if !s.Has(p) {
			t.Errorf("Has(%d) = false, want true", p)
		}
	}
	for _, p := range []int{2999, 3001, 5172, 5184, 8081} {
		if s.Has(p) {
			t.Errorf("Has(%d) = true, want false", p)
		}
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	for _, in := range []string{"0", "65536", "abc", "9000-8000", "3000-"} {
		if _, err := Parse([]string{in}); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

func TestParseSkipsBlanks(t *testing.T) {
	s, err := Parse([]string{"", "  "})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.Empty() {
		t.Errorf("Empty() = false, want true")
	}
	if s.Has(3000) {
		t.Errorf("empty set matched a port")
	}
}
