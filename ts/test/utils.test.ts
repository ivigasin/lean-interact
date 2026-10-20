// Port of LeanInteract tests/test_utils.py.
import assert from "node:assert/strict";
import { test } from "node:test";

import {
  cleanLastTheoremString,
  cleanTheoremString,
  compareLeanVersions,
  compressNewlines,
  extractLastTheorem,
  indentCode,
  leanCommentsRanges,
  parseLeanVersion,
  removeLeanComments,
  splitConclusion,
  splitImplementation,
} from "../src/index.js";

test("indentCode", () => {
  assert.equal(indentCode("theorem my_theorem : True := by\n  sorry", 2), "  theorem my_theorem : True := by\n    sorry");
});

test("leanCommentsRanges", () => {
  const code = "/- This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry";
  const r = leanCommentsRanges(code);
  assert.equal(code.slice(r[0][0], r[0][1]), "/- This is a comment -/");
  assert.equal(code.slice(r[1][0], r[1][1]), "-- single line comment");
});

test("removeLeanComments", () => {
  const code = "/- This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry";
  assert.equal(removeLeanComments(code), "\ntheorem my_theorem : True := by\n\n  sorry");
});

test("removeLeanComments non opened", () => {
  const code = " This is a comment -/\ntheorem my_theorem : True := by\n-- single line comment\n  sorry";
  assert.equal(removeLeanComments(code), "\ntheorem my_theorem : True := by\n\n  sorry");
});

test("removeLeanComments: trailing line comment without newline is removed entirely", () => {
  assert.equal(removeLeanComments("x -- tail"), "x ");
});

test("splitImplementation", () => {
  const decl = "theorem my_theorem : True := by\n  sorry";
  const idx = splitImplementation(decl)!;
  assert.equal(decl.slice(0, idx), "theorem my_theorem : True ");
  assert.equal(decl.slice(idx), ":= by\n  sorry");
});

test("splitConclusion", () => {
  const decl = "theorem my_theorem : True := by\n  sorry";
  const idx = splitConclusion(decl)!;
  assert.equal(decl.slice(0, idx), "theorem my_theorem ");
  assert.equal(decl.slice(idx), ": True := by\n  sorry");
});

test("cleanTheoremString", () => {
  assert.equal(cleanTheoremString("/- comment -/ theorem my_theorem  : \nTrue := by\n  sorry", "dummy", false), "theorem dummy : \nTrue");
});

test("cleanTheoremString partial comment 1", () => {
  assert.equal(cleanTheoremString("comment -/ theorem my_theorem  : \nTrue := by\n  sorry", "dummy", false), "theorem dummy : \nTrue");
});

test("cleanTheoremString partial comment 2", () => {
  const s = `
lemma integrable_x_mul_Smooth1 (SmoothingF : ℝ → ℝ) (ε : ℝ) (hF : ∀ x, 0 ≤ SmoothingF x) (hF' : ContinuousWithDeriv (fun x ↦ SmoothingF x / x) ℝ) (hsupp :
support (fun x ↦ SmoothingF x) = Icc (1/2) 2) (htotal : ∫ x : ℝ in Ioi 0, SmoothingF x / x) = 1) :
  IntegrableOn (fun x : ℝ ↦ x * SmoothingF x) (Ioi 0) :=
  sorry -/`;
  assert.equal(cleanTheoremString(s, "integrable_x_mul_Smooth1", false), null);
});

test("cleanTheoremString hard", () => {
  assert.equal(
    cleanTheoremString("theorem test (h := 1): let x := 1; x = 1 := by\n  sorry", "dummy", false),
    "theorem dummy (h := 1): let x := 1; x = 1",
  );
});

test("cleanTheoremString hard real", () => {
  const s = `theorem nrRealPlaces_eq_zero [IsCyclotomicExtension {n} ℚ K]
  (hn : 2 < n) :
  haveI := IsCyclotomicExtension.numberField {n} ℚ K
  NrRealPlaces K = 0 := by
have := IsCyclotomicExtension.numberField {n} ℚ K
apply (IsCyclotomicExtension.zeta_spec n ℚ K).nrRealPlaces_eq_zero_of_two_lt h`;
  const expected = `theorem dummy [IsCyclotomicExtension {n} ℚ K]
  (hn : 2 < n) :
  haveI := IsCyclotomicExtension.numberField {n} ℚ K
  NrRealPlaces K = 0`;
  assert.equal(cleanTheoremString(s, "dummy", false), expected);
});

test("cleanTheoremString real edge case", () => {
  const s = `lemma ZetaNear1BndExact:
    ∃ (c : ℝ) (_ : 0 < c), ∀ (σ : ℝ) (_ : σ ∈ Ioc 1 2), ‖ζ σ‖ ≤ c / (σ - 1) := by sorry`;
  const expected = `theorem dummy :
    ∃ (c : ℝ) (_ : 0 < c), ∀ (σ : ℝ) (_ : σ ∈ Ioc 1 2), ‖ζ σ‖ ≤ c / (σ - 1)`;
  assert.equal(cleanTheoremString(s, "dummy", false), expected);
});

test("cleanTheoremString addSorry", () => {
  assert.equal(cleanTheoremString("theorem t : True := trivial"), "theorem dummy : True := by sorry");
});

test("extractLastTheorem", () => {
  const code = "/- comment -/ theorem my_theorem : True := by\n  sorry";
  assert.equal(code.slice(extractLastTheorem(code)), "theorem my_theorem : True := by\n  sorry");
  assert.throws(() => extractLastTheorem("def x := 1"));
});

test("cleanLastTheoremString", () => {
  assert.equal(cleanLastTheoremString("/- comment -/\ntheorem my_theorem : True := by\n  sorry", "dummy"), "/- comment -/\ntheorem dummy : True");
});

test("compressNewlines", () => {
  assert.equal(compressNewlines("\n\na\n   \n\n\nb\n\n"), "a\n\nb\n");
});

test("parseLeanVersion", () => {
  assert.equal(parseLeanVersion("leanprover/lean4:v4.19.0"), "v4.19.0");
  assert.equal(parseLeanVersion("v4.8.0-rc1"), "v4.8.0-rc1");
  assert.throws(() => parseLeanVersion("nightly"));
});

test("compareLeanVersions sorts like packaging.version", () => {
  const versions = ["v4.19.0", "v4.9.0", "v4.10.0-rc1", "v4.10.0", "v4.9.0-rc2", "v4.9.0-rc10", "v4.8.0-rc1"];
  assert.deepEqual([...versions].sort(compareLeanVersions), [
    "v4.8.0-rc1",
    "v4.9.0-rc2",
    "v4.9.0-rc10",
    "v4.9.0",
    "v4.10.0-rc1",
    "v4.10.0",
    "v4.19.0",
  ]);
});
