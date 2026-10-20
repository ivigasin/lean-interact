import assert from "node:assert/strict";
import { test } from "node:test";

import {
  command,
  CommandResponse,
  fileCommand,
  InfoTree,
  pickleEnvironment,
  pickleProofState,
  posLe,
  posLt,
  proofStep,
  ProofStepResponse,
  requestKind,
  requestToDict,
  unpickleEnvironment,
  unpickleProofState,
} from "../src/index.js";
import { repoPathParts } from "../src/internal/paths.js";

test("requestKind from tags and from keys", () => {
  assert.equal(requestKind(command({ cmd: "x" })), "Command");
  assert.equal(requestKind(fileCommand({ path: "a.lean" })), "FileCommand");
  assert.equal(requestKind(proofStep({ proofState: 0, tactic: "rfl" })), "ProofStep");
  assert.equal(requestKind(pickleEnvironment({ env: 0, pickleTo: "a" })), "PickleEnvironment");
  assert.equal(requestKind(pickleProofState({ proofState: 0, pickleTo: "a" })), "PickleProofState");
  assert.equal(requestKind(unpickleEnvironment({ unpickleEnvFrom: "a" })), "UnpickleEnvironment");
  assert.equal(requestKind(unpickleProofState({ unpickleProofStateFrom: "a" })), "UnpickleProofState");
  assert.equal(requestKind({ pickleTo: "a", proofState: 1 }), "PickleProofState");
  assert.equal(requestKind({ pickleTo: "a", env: 1 }), "PickleEnvironment");
  assert.throws(() => requestKind({}));
});

test("constructors validate min_length", () => {
  assert.throws(() => command({ cmd: "" }));
  assert.throws(() => proofStep({ proofState: 0, tactic: "" }));
});

test("requestToDict drops the tag and unset fields", () => {
  const dict = requestToDict(command({ cmd: "def x := 1", env: undefined, allTactics: true }));
  assert.deepEqual(dict, { cmd: "def x := 1", allTactics: true });
  assert.equal(JSON.stringify(command({ cmd: "a", rootGoals: false })), '{"cmd":"a","rootGoals":false}');
});

test("Pos ordering", () => {
  assert.ok(posLe({ line: 1, column: 2 }, { line: 1, column: 2 }));
  assert.ok(!posLt({ line: 1, column: 2 }, { line: 1, column: 2 }));
  assert.ok(posLt({ line: 1, column: 9 }, { line: 2, column: 0 }));
  assert.ok(!posLe({ line: 3, column: 0 }, { line: 2, column: 9 }));
});

test("CommandResponse with sorry (shape from test_run_tactic)", () => {
  const res = new CommandResponse({
    env: 0,
    messages: [{ pos: { line: 1, column: 8 }, endPos: { line: 1, column: 20 }, severity: "warning", data: "declaration uses `sorry`" }],
    sorries: [{ proofState: 0, pos: { line: 1, column: 32 }, endPos: { line: 1, column: 37 }, goal: "⊢ 0 = 0" }],
  });
  assert.equal(res.env, 0);
  assert.equal(res.sorries[0].proofState, 0);
  assert.equal(res.getWarnings().length, 1);
  assert.equal(res.getErrors().length, 0);
  assert.ok(!res.hasErrors());
  assert.ok(res.leanCodeIsValid());
  assert.ok(!res.leanCodeIsValid({ allowSorry: false }));
  // Outside the line range, the sorry does not count.
  assert.ok(res.leanCodeIsValid({ startPos: { line: 2, column: 0 }, allowSorry: false }));
  assert.deepEqual(res.tactics, []);
  assert.equal(res.infotree, null);
  assert.equal(res.with({ env: -1 }).env, -1);
});

test("CommandResponse keeps unknown keys", () => {
  const res = new CommandResponse({ env: 3, somethingNew: { a: 1 } });
  assert.deepEqual(res.somethingNew, { a: 1 });
});

test("ProofStepResponse (shape from test_run_tactic)", () => {
  const res = new ProofStepResponse({ proofState: 1, goals: [], proofStatus: "Completed" });
  assert.equal(res.proofState, 1);
  assert.deepEqual(res.traces, []);
  assert.throws(() => new ProofStepResponse({ proofStatus: "x" } as never));
});

test("DeclarationInfo parsing (shape from test_declaration_info)", () => {
  const res = new CommandResponse({
    env: 1,
    declarations: [
      {
        pp: "theorem test (h : p) : 0 = 0 := by rfl",
        range: { synthetic: false, start: { line: 2, column: 0 }, finish: { line: 2, column: 38 } },
        scope: { varDecls: ["variable (p : Prop)"], currNamespace: "[anonymous]" },
        name: "test",
        fullName: "test",
        kind: "theorem",
        modifiers: { isUnsafe: false },
        signature: { pp: "(h : p) : 0 = 0", constants: ["h", "p"], range: { synthetic: false, start: { line: 2, column: 13 }, finish: { line: 2, column: 28 } } },
        binders: {
          pp: "(h : p)",
          groups: ["(h : p)"],
          map: [{ id: "h", type: "p", binderInfo: "default" }],
          range: { synthetic: false, start: { line: 2, column: 13 }, finish: { line: 2, column: 20 } },
        },
        type: { pp: "0 = 0", constants: [], range: { synthetic: false, start: { line: 2, column: 23 }, finish: { line: 2, column: 28 } } },
        value: { pp: ":= by rfl", constants: [], range: { synthetic: false, start: { line: 2, column: 29 }, finish: { line: 2, column: 38 } } },
      },
    ],
  });
  const d = res.declarations[0];
  assert.equal(d.fullName, "test");
  assert.equal(d.binders!.map[0].binderInfo, "default");
  // Defaults filled in like pydantic.
  assert.deepEqual(d.modifiers, {
    isUnsafe: false,
    docString: null,
    visibility: "regular",
    computeKind: "regular",
    recKind: "default",
    isProtected: false,
    attributes: [],
  });
  assert.deepEqual(d.scope.openDecl, []);
  assert.deepEqual(d.scope.levelNames, []);
});

function cmdNode(elaborator: string, kind: string, argKinds: string[], start: [number, number], finish: [number, number], pp = "") {
  return {
    kind: "CommandInfo" as const,
    node: {
      elaborator,
      stx: { pp, kind, argKinds, range: { synthetic: false, start: { line: start[0], column: start[1] }, finish: { line: finish[0], column: finish[1] } } },
    },
    children: [] as unknown[],
  };
}

test("InfoTree traversal and helpers", () => {
  const thm = cmdNode(
    "Lean.Elab.Command.elabDeclaration",
    "Lean.Parser.Command.declaration",
    ["Lean.Parser.Command.declModifiers", "Lean.Parser.Command.theorem"],
    [1, 0],
    [2, 9],
    "theorem t : 0 = 0 := by\n  sorry",
  );
  const tactic = {
    kind: "TacticInfo",
    node: { name: "Lean.Parser.Tactic.tacticSorry", stx: { pp: "sorry", kind: "x", range: { synthetic: false, start: { line: 2, column: 2 }, finish: { line: 2, column: 7 } } } },
    children: [],
  };
  thm.children = [tactic];
  const root = new InfoTree({
    kind: "CommandInfo",
    node: cmdNode("Lean.Elab.Command.elabVariable", "Lean.Parser.Command.variable", [], [1, 0], [1, 5]).node,
    children: [thm, cmdNode("Lean.Elab.Command.elabNamespace", "ns", [], [3, 0], [3, 5]), cmdNode("Lean.Elab.Command.elabModuleDoc", "doc", [], [4, 0], [4, 5])],
  });

  assert.equal([...root.dfsWalk()].length, 5);
  // Python's deque.extendleft visits siblings last-to-first.
  assert.deepEqual(
    [...root.dfsWalk()].map((t) => (t.node as { elaborator?: string }).elaborator ?? t.kind),
    ["Lean.Elab.Command.elabVariable", "Lean.Elab.Command.elabModuleDoc", "Lean.Elab.Command.elabNamespace", "Lean.Elab.Command.elabDeclaration", "TacticInfo"],
  );
  assert.equal([...root.leaves()].length, 3);
  assert.equal([...root.commands()].length, 4);
  assert.equal([...root.variables()].length, 1);
  assert.equal([...root.theorems()].length, 1);
  assert.equal([...root.docs()].length, 1);
  assert.equal([...root.namespaces()].length, 1);
  assert.ok(root.children[0].children[0] instanceof InfoTree);
  assert.deepEqual((root.children[0].children[0].node as { goalsBefore: string[] }).goalsBefore, []);

  const found = root.theoremForSorry({ pos: { line: 2, column: 2 }, endPos: { line: 2, column: 7 }, goal: "⊢ 0 = 0" });
  assert.equal(found, root.children[0]);
  assert.equal(root.theoremForSorry({ pos: { line: 5, column: 0 }, endPos: { line: 5, column: 5 }, goal: "" }), null);

  const t = root.children[0];
  assert.equal(t.ppUpTo({ line: 2, column: 4 }), "theorem t : 0 = 0 := by\n  so");
  assert.equal(t.ppUpTo({ line: 1, column: 9 }), "theorem t");
  assert.throws(() => t.ppUpTo({ line: 3, column: 0 }));
});

test("CommandResponse builds InfoTree instances", () => {
  const res = new CommandResponse({ env: 0, infotree: [{ kind: "CommandInfo", node: null, children: [] }] });
  assert.ok(res.infotree![0] instanceof InfoTree);
});

test("repoPathParts handles URLs, scp-style remotes and Windows paths", () => {
  const cases: Array<[string, string[]]> = [
    ["https://github.com/augustepoiroux/repl", ["augustepoiroux", "repl"]],
    ["https://github.com/augustepoiroux/repl.git", ["augustepoiroux", "repl"]],
    ["git@github.com:owner/repo.git", ["git@github.com_owner", "repo"]],
    ["C:\\Users\\runner\\Temp\\owner\\repl", ["owner", "repl"]],
    ["C:\\Users\\runner\\Temp\\owner\\repl.git", ["owner", "repl"]],
    ["/tmp/owner/repl", ["owner", "repl"]],
    ["repl.git", ["repl"]],
  ];
  for (const [url, want] of cases) assert.deepEqual(repoPathParts(url), want, url);
});
