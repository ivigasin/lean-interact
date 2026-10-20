package leaninteract

import (
	"path/filepath"
	"testing"
)

// Ported from LeanInteract tests/test_utils.py.

func TestIndentCode(t *testing.T) {
	got := IndentCode("theorem my_theorem : True := by\n  sorry", 2)
	if want := "  theorem my_theorem : True := by\n    sorry"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLeanCommentsRanges(t *testing.T) {
	code := "/- This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry"
	r, err := LeanCommentsRanges(code, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := code[r[0].Start:r[0].End]; got != "/- This is a comment -/" {
		t.Fatalf("first comment %q", got)
	}
	if got := code[r[1].Start:r[1].End]; got != "-- single line comment" {
		t.Fatalf("second comment %q", got)
	}
}

func TestRemoveLeanComments(t *testing.T) {
	for _, code := range []string{
		"/- This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry",
		" This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry",
	} {
		got, ok := RemoveLeanComments(code)
		if want := "\ntheorem my_theorem : True := by\n\n  sorry"; !ok || got != want {
			t.Fatalf("got %q (%v), want %q", got, ok, want)
		}
	}
}

func TestRemoveLeanCommentsUnterminatedLineComment(t *testing.T) {
	got, ok := RemoveLeanComments("def x := 1 -- trailing")
	if !ok || got != "def x := 1 " {
		t.Fatalf("got %q", got)
	}
}

func TestSplitImplementation(t *testing.T) {
	decl := "theorem my_theorem : True := by\n  sorry"
	i, ok := SplitImplementation(decl, 0)
	if !ok || decl[:i] != "theorem my_theorem : True " || decl[i:] != ":= by\n  sorry" {
		t.Fatalf("bad split at %d", i)
	}
}

func TestSplitConclusion(t *testing.T) {
	decl := "theorem my_theorem : True := by\n  sorry"
	i, ok := SplitConclusion(decl, 0)
	if !ok || decl[:i] != "theorem my_theorem " || decl[i:] != ": True := by\n  sorry" {
		t.Fatalf("bad split at %d", i)
	}
}

func TestCleanTheoremString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/- comment -/ theorem my_theorem  : \nTrue := by\n  sorry", "theorem dummy : \nTrue"},
		{"comment -/ theorem my_theorem  : \nTrue := by\n  sorry", "theorem dummy : \nTrue"},
		{"theorem test (h := 1): let x := 1; x = 1 := by\n  sorry", "theorem dummy (h := 1): let x := 1; x = 1"},
		{`theorem nrRealPlaces_eq_zero [IsCyclotomicExtension {n} ℚ K]
  (hn : 2 < n) :
  haveI := IsCyclotomicExtension.numberField {n} ℚ K
  NrRealPlaces K = 0 := by
have := IsCyclotomicExtension.numberField {n} ℚ K
apply (IsCyclotomicExtension.zeta_spec n ℚ K).nrRealPlaces_eq_zero_of_two_lt h`, `theorem dummy [IsCyclotomicExtension {n} ℚ K]
  (hn : 2 < n) :
  haveI := IsCyclotomicExtension.numberField {n} ℚ K
  NrRealPlaces K = 0`},
		{`lemma ZetaNear1BndExact:
    ∃ (c : ℝ) (_ : 0 < c), ∀ (σ : ℝ) (_ : σ ∈ Ioc 1 2), ‖ζ σ‖ ≤ c / (σ - 1) := by sorry`, `theorem dummy :
    ∃ (c : ℝ) (_ : 0 < c), ∀ (σ : ℝ) (_ : σ ∈ Ioc 1 2), ‖ζ σ‖ ≤ c / (σ - 1)`},
	}
	for _, c := range cases {
		got, ok := CleanTheoremString(c.in, "dummy", false)
		if !ok || got != c.want {
			t.Errorf("CleanTheoremString(%q) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

func TestCleanTheoremStringPartialComment(t *testing.T) {
	in := `
lemma integrable_x_mul_Smooth1 (SmoothingF : ℝ → ℝ) (ε : ℝ) (hF : ∀ x, 0 ≤ SmoothingF x) (hF' : ContinuousWithDeriv (fun x ↦ SmoothingF x / x) ℝ) (hsupp :
support (fun x ↦ SmoothingF x) = Icc (1/2) 2) (htotal : ∫ x : ℝ in Ioi 0, SmoothingF x / x) = 1) :
  IntegrableOn (fun x : ℝ ↦ x * SmoothingF x) (Ioi 0) :=
  sorry -/`
	if got, ok := CleanTheoremString(in, "integrable_x_mul_Smooth1", false); ok {
		t.Fatalf("expected failure, got %q", got)
	}
}

func TestCleanTheoremStringAddSorry(t *testing.T) {
	got, ok := CleanTheoremString("example : 1 = 1 := rfl", "foo", true)
	if want := "theorem foo : 1 = 1 := by sorry"; !ok || got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtractLastTheorem(t *testing.T) {
	code := "/- comment -/ theorem my_theorem : True := by\n  sorry"
	i, err := ExtractLastTheorem(code)
	if err != nil || code[i:] != "theorem my_theorem : True := by\n  sorry" {
		t.Fatalf("got %q, %v", code[i:], err)
	}
	if _, err := ExtractLastTheorem("def x := 1"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCleanLastTheoremString(t *testing.T) {
	got, err := CleanLastTheoremString("/- comment -/\ntheorem my_theorem : True := by\n  sorry", "dummy", false)
	if want := "/- comment -/\ntheorem dummy : True"; err != nil || got != want {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestCompressNewlines(t *testing.T) {
	got := CompressNewlines("\n\n  a\n   \n\n\nb\n\n")
	if want := "a\n\nb\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVersions(t *testing.T) {
	vs := []string{"v4.10.0", "v4.9.0", "v4.9.0-rc1", "v4.19.0-rc3", "v4.8.0-rc1", "v4.19.0", "v4.9.0-rc2"}
	SortLeanVersions(vs)
	want := []string{"v4.8.0-rc1", "v4.9.0-rc1", "v4.9.0-rc2", "v4.9.0", "v4.10.0", "v4.19.0-rc3", "v4.19.0"}
	for i := range want {
		if vs[i] != want[i] {
			t.Fatalf("got %v, want %v", vs, want)
		}
	}
	if v, err := ParseLeanVersion("leanprover/lean4:v4.19.0"); err != nil || v != "v4.19.0" {
		t.Fatalf("ParseLeanVersion: %q %v", v, err)
	}
	if _, err := ParseLeanVersion("v3.0"); err == nil {
		t.Fatal("expected error for Lean 3")
	}
	if lakeInitTemplate("v4.7.0") != "exe" || lakeInitTemplate("v4.8.0-rc1") != "exe.lean" {
		t.Fatal("lake init template")
	}
}

func TestRepoNameFromURL(t *testing.T) {
	for url, want := range map[string]string{
		"https://github.com/augustepoiroux/repl":     filepath.Join("augustepoiroux", "repl"),
		"https://github.com/augustepoiroux/repl.git": filepath.Join("augustepoiroux", "repl"),
		"git@github.com:owner/repo.git":              filepath.Join("git@github.com_owner", "repo"),
		`C:\Users\runner\Temp\owner\repl`:            filepath.Join("owner", "repl"),
		`C:\Users\runner\Temp\owner\repl.git`:        filepath.Join("owner", "repl"),
		"/tmp/owner/repl":                            filepath.Join("owner", "repl"),
		"repl.git":                                   "repl",
	} {
		if got := repoNameFromURL(url); got != want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", url, got, want)
		}
	}
}
