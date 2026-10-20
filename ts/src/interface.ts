/**
 * Request and response data models for the Lean REPL.
 *
 * Port of `lean_interact.interface`. Field names are the REPL wire names (camelCase), so a request
 * object is exactly the JSON sent to the REPL and a response object keeps every key the REPL
 * returned (unknown keys included, like pydantic's `extra="allow"`).
 *
 * Aligned with the Lean REPL API:
 * https://github.com/leanprover-community/repl/blob/master/REPL/JSON.lean
 *
 * @module
 */

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

/** A Lean name, as a list of components (`["Elab", "async"]`). */
export type Name = string[];

export type DataValue = boolean | number | string | Name;

/** Options passed to `set_option` before running a command. */
export type Options = Array<[Name, DataValue]>;

/** InfoTree detail levels. */
export const InfoTreeOptions = {
  /** No filtering: the entire InfoTree. */
  full: "full",
  /** Only nodes produced by tactics. */
  tactics: "tactics",
  /** Tactic nodes, restricted to their "original" (non-synthetic) sub-parts. */
  original: "original",
  /** Substantive tactic content, without pure combinators (`by`, `;`, parentheses...). */
  substantive: "substantive",
} as const;
export type InfoTreeOption = (typeof InfoTreeOptions)[keyof typeof InfoTreeOptions];

/** Common options for {@link Command} and {@link FileCommand}. */
export interface CommandOptions {
  /** Return all tactics used in the command with their associated information. */
  allTactics?: boolean;
  /** Return detailed information about declarations in the command. */
  declarations?: boolean;
  /** Return root goals, i.e. initial goals of all declarations, even if they already have a proof. */
  rootGoals?: boolean;
  /** Return syntax information. One of {@link InfoTreeOptions}; anything else is ignored by the REPL. */
  infotree?: InfoTreeOption | (string & {});
  /** Enable incremental optimization for the command. */
  incrementality?: boolean;
  /** Options set before executing the command (`set_option` in Lean). */
  setOptions?: Options;
}

/** Symbol used to tag requests built with the constructor helpers. Not serialized. */
export const REQUEST_KIND: unique symbol = Symbol.for("lean-interact.requestKind");

export type RequestKind =
  | "Command"
  | "FileCommand"
  | "ProofStep"
  | "PickleEnvironment"
  | "UnpickleEnvironment"
  | "PickleProofState"
  | "UnpickleProofState";

interface Tagged<K extends RequestKind> {
  readonly [REQUEST_KIND]?: K;
}

/** Command to be executed in the REPL. */
export interface Command extends CommandOptions, Tagged<"Command"> {
  /** The Lean code to execute. */
  cmd: string;
  /** Environment to run in. Omit to start a new session (in which `import` is allowed). */
  env?: number;
}

/** Run a Lean file. */
export interface FileCommand extends CommandOptions, Tagged<"FileCommand"> {
  /** Path of the file, relative to the working directory. */
  path: string;
  env?: number;
}

/** Apply a tactic to a proof state. */
export interface ProofStep extends Tagged<"ProofStep"> {
  proofState: number;
  tactic: string;
}

/** Pickle an environment to disk. */
export interface PickleEnvironment extends Tagged<"PickleEnvironment"> {
  env: number;
  pickleTo: string;
}

/** Load a pickled environment. */
export interface UnpickleEnvironment extends Tagged<"UnpickleEnvironment"> {
  unpickleEnvFrom: string;
}

/** Pickle a proof state to disk. */
export interface PickleProofState extends Tagged<"PickleProofState"> {
  proofState: number;
  pickleTo: string;
}

/** Load a pickled proof state. */
export interface UnpickleProofState extends Tagged<"UnpickleProofState"> {
  unpickleProofStateFrom: string;
  /** Environment used as context for unpickling. */
  env?: number;
}

/** Requests answered with a {@link CommandResponse}. */
export type CommandLikeRequest = Command | FileCommand | PickleEnvironment | UnpickleEnvironment;
/** Requests answered with a {@link ProofStepResponse}. */
export type ProofStepLikeRequest = ProofStep | PickleProofState | UnpickleProofState;
/** Any typed REPL request. */
export type REPLRequest = CommandLikeRequest | ProofStepLikeRequest;

function nonEmpty(value: unknown, field: string, kind: string): void {
  if (typeof value !== "string" || value.length === 0) {
    throw new TypeError(`${kind}.${field} must be a non-empty string`);
  }
}

function tag<K extends RequestKind, T extends object>(kind: K, value: T): T & { readonly [REQUEST_KIND]: K } {
  return Object.assign({ ...value }, { [REQUEST_KIND]: kind }) as T & { readonly [REQUEST_KIND]: K };
}

/** Build a validated {@link Command}. */
export function command(value: Omit<Command, typeof REQUEST_KIND>): Command {
  nonEmpty(value.cmd, "cmd", "Command");
  return tag("Command", value);
}

/** Build a validated {@link FileCommand}. */
export function fileCommand(value: Omit<FileCommand, typeof REQUEST_KIND>): FileCommand {
  nonEmpty(value.path, "path", "FileCommand");
  return tag("FileCommand", value);
}

/** Build a validated {@link ProofStep}. */
export function proofStep(value: Omit<ProofStep, typeof REQUEST_KIND>): ProofStep {
  nonEmpty(value.tactic, "tactic", "ProofStep");
  return tag("ProofStep", value);
}

/** Build a validated {@link PickleEnvironment}. */
export function pickleEnvironment(value: Omit<PickleEnvironment, typeof REQUEST_KIND>): PickleEnvironment {
  nonEmpty(value.pickleTo, "pickleTo", "PickleEnvironment");
  return tag("PickleEnvironment", value);
}

/** Build a validated {@link UnpickleEnvironment}. */
export function unpickleEnvironment(value: Omit<UnpickleEnvironment, typeof REQUEST_KIND>): UnpickleEnvironment {
  nonEmpty(value.unpickleEnvFrom, "unpickleEnvFrom", "UnpickleEnvironment");
  return tag("UnpickleEnvironment", value);
}

/** Build a validated {@link PickleProofState}. */
export function pickleProofState(value: Omit<PickleProofState, typeof REQUEST_KIND>): PickleProofState {
  nonEmpty(value.pickleTo, "pickleTo", "PickleProofState");
  return tag("PickleProofState", value);
}

/** Build a validated {@link UnpickleProofState}. */
export function unpickleProofState(value: Omit<UnpickleProofState, typeof REQUEST_KIND>): UnpickleProofState {
  nonEmpty(value.unpickleProofStateFrom, "unpickleProofStateFrom", "UnpickleProofState");
  return tag("UnpickleProofState", value);
}

/**
 * Determine the kind of a request: from its tag when built with a constructor helper,
 * otherwise from its keys.
 */
export function requestKind(request: object): RequestKind {
  const tagged = (request as Tagged<RequestKind>)[REQUEST_KIND];
  if (tagged) return tagged;
  const r = request as Record<string, unknown>;
  if ("cmd" in r) return "Command";
  if ("path" in r) return "FileCommand";
  if ("tactic" in r) return "ProofStep";
  if ("unpickleEnvFrom" in r) return "UnpickleEnvironment";
  if ("unpickleProofStateFrom" in r) return "UnpickleProofState";
  if ("pickleTo" in r) return r.proofState !== undefined && r.proofState !== null ? "PickleProofState" : "PickleEnvironment";
  throw new TypeError(`Unrecognized Lean REPL request: ${JSON.stringify(request)}`);
}

/** Whether a request kind is answered with a {@link CommandResponse}. */
export function isCommandLikeKind(kind: RequestKind): boolean {
  return kind === "Command" || kind === "FileCommand" || kind === "PickleEnvironment" || kind === "UnpickleEnvironment";
}

/**
 * The JSON object sent to the REPL for a request: tag removed, `undefined`/`null` top-level fields
 * dropped (Python's `model_dump(exclude_none=True, by_alias=True)`).
 */
export function requestToDict(request: object): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(request)) {
    if (v !== undefined && v !== null) out[k] = v;
  }
  return out;
}

/** Deep copy of a request, preserving its kind tag. */
export function cloneRequest<T extends object>(request: T): T {
  const kind = requestKind(request);
  return Object.assign(structuredClone(requestToDict(request)), { [REQUEST_KIND]: kind }) as unknown as T;
}

// ---------------------------------------------------------------------------
// Intermediate structures
// ---------------------------------------------------------------------------

/** Position in Lean code (1-based line, 0-based column). */
export interface Pos {
  line: number;
  column: number;
}

export function posEq(a: Pos, b: Pos): boolean {
  return a.line === b.line && a.column === b.column;
}

/** `a <= b` in source order. */
export function posLe(a: Pos, b: Pos): boolean {
  if (a.line < b.line) return true;
  if (a.line === b.line) return a.column <= b.column;
  return false;
}

/** `a < b` in source order. */
export function posLt(a: Pos, b: Pos): boolean {
  return posLe(a, b) && !posEq(a, b);
}

export type Severity = "error" | "warning" | "info" | "trace";

/** A message (error, warning, ...) reported by Lean. */
export interface Message {
  /** Start position. Python name: `start_pos`. */
  pos: Pos;
  /** End position. Python name: `end_pos`. */
  endPos?: Pos | null;
  severity: Severity;
  data: string;
}

/** A `sorry` found in the submitted code. */
export interface Sorry {
  pos?: Pos | null;
  endPos?: Pos | null;
  /** The proof goal at the sorry location. */
  goal: string;
  /** Proof state you can continue from with a {@link ProofStep}. */
  proofState?: number | null;
}

/** A tactic occurrence (returned when `allTactics` is set). */
export interface Tactic {
  pos: Pos;
  endPos: Pos;
  goals: string;
  tactic: string;
  proofState?: number | null;
  usedConstants: string[];
}

/** Whether a message or sorry intersects the lines between `startPos` and `endPos`. */
export function messageIntersectsCode(msg: Message | Sorry, startPos?: Pos | null, endPos?: Pos | null): boolean {
  let res = true;
  if (startPos && msg.endPos) res = res && msg.endPos.line >= startPos.line;
  if (endPos && msg.pos) res = res && msg.pos.line <= endPos.line;
  return res;
}

/** Range of a syntax object. */
export interface Range {
  synthetic: boolean;
  start: Pos;
  finish: Pos;
}

/** Range equality ignores `synthetic`, as in Python. */
export function rangeEq(a: Range, b: Range): boolean {
  return posEq(a.start, b.start) && posEq(a.finish, b.finish);
}

/** A Lean `Syntax` object. */
export interface Syntax {
  pp: string | null;
  range: Range;
  kind: string;
  argKinds: string[];
}

export interface TacticNode {
  stx: Syntax;
  name: string | null;
  goalsBefore: string[];
  goalsAfter: string[];
}

export interface CommandNode {
  stx: Syntax;
  elaborator: string;
}

export interface TermNode {
  stx: Syntax;
  isBinder: boolean;
  expr: string;
  expectedType?: string | null;
  elaborator: string | null;
}

/** A node of the InfoTree. */
export type InfoNode = TacticNode | CommandNode | TermNode | null;

export type InfoTreeKind =
  | "TacticInfo"
  | "TermInfo"
  | "PartialTermInfo"
  | "CommandInfo"
  | "MacroExpansionInfo"
  | "OptionInfo"
  | "FieldInfo"
  | "CompletionInfo"
  | "UserWidgetInfo"
  | "CustomInfo"
  | "FVarAliasInfo"
  | "FieldRedeclInfo"
  | "ChoiceInfo"
  | "DelabTermInfo";

function normalizeSyntax(stx: Syntax): Syntax {
  return { ...stx, argKinds: stx.argKinds ?? [] };
}

function normalizeNode(node: unknown): InfoNode {
  if (node === null || node === undefined) return null;
  const n = { ...(node as Record<string, unknown>) };
  if (n.stx) n.stx = normalizeSyntax(n.stx as Syntax);
  if ("goalsBefore" in n || "goalsAfter" in n || "name" in n) {
    n.goalsBefore ??= [];
    n.goalsAfter ??= [];
  }
  return n as unknown as InfoNode;
}

/** An InfoTree of elaborated Lean code. */
export class InfoTree {
  node: InfoNode;
  kind: InfoTreeKind;
  children: InfoTree[];
  [key: string]: unknown;

  constructor(data: { node?: unknown; kind: InfoTreeKind; children?: unknown[]; [key: string]: unknown }) {
    Object.assign(this, data);
    this.node = normalizeNode(data.node);
    this.kind = data.kind;
    this.children = (data.children ?? []).map((c) => (c instanceof InfoTree ? c : new InfoTree(c as never)));
  }

  /**
   * Walk the tree depth-first, iteratively. Matches Python's `deque.extendleft` traversal exactly,
   * which visits siblings last-to-first.
   */
  *dfsWalk(): Generator<InfoTree> {
    const stack: InfoTree[] = [this];
    while (stack.length > 0) {
      const first = stack.pop()!;
      yield first;
      for (const child of first.children) stack.push(child);
    }
  }

  /** Leaves of the tree, in DFS order. */
  *leaves(): Generator<InfoTree> {
    for (const tree of this.dfsWalk()) if (tree.children.length === 0) yield tree;
  }

  /** Subtrees representing commands. */
  *commands(): Generator<InfoTree & { node: CommandNode }> {
    for (const tree of this.dfsWalk()) {
      if (tree.kind !== "CommandInfo") continue;
      yield tree as InfoTree & { node: CommandNode };
    }
  }

  /** Subtrees for `variable` commands. */
  *variables(): Generator<InfoTree & { node: CommandNode }> {
    for (const tree of this.commands()) {
      if (tree.node.elaborator === "Lean.Elab.Command.elabVariable") yield tree;
    }
  }

  /** Subtrees for theorems (including lemmas). */
  *theorems(): Generator<InfoTree & { node: CommandNode }> {
    for (const tree of this.commands()) {
      if (tree.node.stx.kind !== "Lean.Parser.Command.declaration") continue;
      if (tree.node.stx.argKinds.at(-1) !== "Lean.Parser.Command.theorem") continue;
      yield tree;
    }
  }

  /** Subtrees for module docstrings. */
  *docs(): Generator<InfoTree & { node: CommandNode }> {
    for (const tree of this.commands()) {
      if (tree.node.elaborator === "Lean.Elab.Command.elabModuleDoc") yield tree;
    }
  }

  /** Subtrees for namespaces. */
  *namespaces(): Generator<InfoTree & { node: CommandNode }> {
    for (const tree of this.commands()) {
      if (tree.node.elaborator === "Lean.Elab.Command.elabNamespace") yield tree;
    }
  }

  /** Pretty-printed source of this node up to `endPos`. */
  ppUpTo(endPos: Pos): string {
    if (this.node === null) throw new Error("InfoTree node is null, cannot pretty-print!");
    const range = this.node.stx.range;
    if (posLt(range.finish, endPos) || posLt(endPos, range.start)) throw new RangeError("endPos has to be in bounds!");
    if (this.node.stx.pp === null || this.node.stx.pp === undefined) {
      throw new Error("InfoTree node has no pretty-printed string!");
    }
    const lines = splitLinesKeepEnds(this.node.stx.pp);
    const result: string[] = [];
    for (let lineIdx = 0; lineIdx < endPos.line + 1 - range.start.line; lineIdx++) {
      let line = lines[lineIdx];
      if (lineIdx === endPos.line - range.start.line) line = line.slice(0, endPos.column);
      result.push(line);
    }
    return result.join("");
  }

  /** The theorem subtree containing a given sorry, if any. */
  theoremForSorry(sorry: Sorry): (InfoTree & { node: CommandNode }) | null {
    let found: (InfoTree & { node: CommandNode }) | null = null;
    for (const tree of this.theorems()) {
      const thmRange = tree.node.stx.range;
      if (!sorry.pos || !sorry.endPos) continue;
      if (posLt(sorry.pos, thmRange.start) || posLt(thmRange.finish, sorry.endPos)) continue;
      if (found !== null) throw new Error("Multiple theorems contain the same sorry");
      found = tree;
    }
    return found;
  }
}

/** Python's `str.splitlines(keepends=True)`. */
const LINE_BREAKS = "\\n\\r\\v\\f\\x1c-\\x1e\\x85\\u2028\\u2029";
const SPLIT_LINES_RE = new RegExp(`[^${LINE_BREAKS}]*(?:\\r\\n|[${LINE_BREAKS}])|[^${LINE_BREAKS}]+$`, "g");

function splitLinesKeepEnds(text: string): string[] {
  return text.match(SPLIT_LINES_RE) ?? [];
}

export interface DocString {
  content: string;
  range: Range;
}

export interface DeclModifiers {
  docString?: DocString | null;
  visibility: "regular" | "private" | "protected" | "public";
  computeKind: "regular" | "meta" | "noncomputable";
  recKind: "default" | "partial" | "nonrec";
  isProtected: boolean;
  isUnsafe: boolean;
  attributes: string[];
}

export interface DeclSignature {
  pp: string;
  constants: string[];
  range: Range;
}

export interface BinderView {
  id: string;
  type: string;
  binderInfo: string;
}

export interface DeclBinders {
  pp: string;
  groups: string[];
  map: BinderView[];
  range: Range;
}

export interface DeclType {
  pp: string;
  constants: string[];
  range: Range;
}

export interface DeclValue {
  pp: string;
  constants: string[];
  range: Range;
}

export interface OpenDecl {
  simple?: Record<string, string | string[]> | null;
  rename?: Record<string, string> | null;
}

export interface ScopeInfo {
  varDecls: string[];
  includeVars: string[];
  omitVars: string[];
  levelNames: string[];
  currNamespace: string;
  openDecl: OpenDecl[];
}

/** Detailed information about a declaration (returned when `declarations` is set). */
export interface DeclarationInfo {
  pp: string;
  range: Range;
  scope: ScopeInfo;
  name: string;
  fullName: string;
  kind: string;
  modifiers: DeclModifiers;
  signature: DeclSignature;
  binders?: DeclBinders | null;
  type?: DeclType | null;
  value?: DeclValue | null;
}

function normalizeDeclaration(d: DeclarationInfo): DeclarationInfo {
  const m = (d.modifiers ?? {}) as Partial<DeclModifiers>;
  const s = (d.scope ?? {}) as Partial<ScopeInfo>;
  return {
    ...d,
    modifiers: {
      ...m,
      docString: m.docString ?? null,
      visibility: m.visibility ?? "regular",
      computeKind: m.computeKind ?? "regular",
      recKind: m.recKind ?? "default",
      isProtected: m.isProtected ?? false,
      isUnsafe: m.isUnsafe ?? false,
      attributes: m.attributes ?? [],
    },
    scope: {
      ...s,
      varDecls: s.varDecls ?? [],
      includeVars: s.includeVars ?? [],
      omitVars: s.omitVars ?? [],
      levelNames: s.levelNames ?? [],
      currNamespace: s.currNamespace as string,
      openDecl: s.openDecl ?? [],
    },
  };
}

// ---------------------------------------------------------------------------
// Responses
// ---------------------------------------------------------------------------

function isDeclarationUsesSorry(data: string): boolean {
  return data === "declaration uses 'sorry'" || data === "declaration uses `sorry`";
}

/** Base class for REPL responses. Unknown REPL keys are kept as own properties. */
export abstract class BaseREPLResponse {
  /** Messages reported by Lean. */
  messages: Message[] = [];
  /** Sorries found in the submitted code. */
  sorries: Sorry[] = [];

  protected init(data: Record<string, unknown>): void {
    Object.assign(this, data);
    this.messages = (data.messages as Message[] | undefined) ?? [];
    this.sorries = (data.sorries as Sorry[] | undefined) ?? [];
  }

  /** All error messages. */
  getErrors(): Message[] {
    return this.messages.filter((m) => m.severity === "error");
  }

  /** All warning messages. */
  getWarnings(): Message[] {
    return this.messages.filter((m) => m.severity === "warning");
  }

  /** Whether the response contains any error message. */
  hasErrors(): boolean {
    return this.messages.some((m) => m.severity === "error");
  }

  /**
   * Whether the submitted code is valid Lean code, only considering messages that intersect the
   * lines between `startPos` and `endPos`.
   */
  leanCodeIsValid(options: { startPos?: Pos | null; endPos?: Pos | null; allowSorry?: boolean } = {}): boolean {
    const { startPos = null, endPos = null, allowSorry = true } = options;
    const errors = this.messages.filter((m) => messageIntersectsCode(m, startPos, endPos) && m.severity === "error");
    const sorries = [
      ...this.sorries.filter((s) => messageIntersectsCode(s, startPos, endPos)),
      ...this.messages.filter((m) => messageIntersectsCode(m, startPos, endPos) && isDeclarationUsesSorry(m.data)),
    ];
    return errors.length === 0 && (allowSorry || sorries.length === 0);
  }

  /** Plain JSON view (wire names). */
  toJSON(): Record<string, unknown> {
    return { ...this } as Record<string, unknown>;
  }
}

/** Response to a {@link Command}, {@link FileCommand} or environment (un)pickling. */
export class CommandResponse extends BaseREPLResponse {
  /** The new environment after running the command. */
  env!: number;
  /** Tactics in the code (only with `allTactics`). */
  tactics: Tactic[] = [];
  /** Declarations in the code (only with `declarations`). */
  declarations: DeclarationInfo[] = [];
  /** InfoTrees of the code (only with `infotree`). */
  infotree: InfoTree[] | null = null;
  [key: string]: unknown;

  constructor(data: { env: number; [key: string]: unknown }) {
    super();
    this.init(data);
    if (typeof data.env !== "number") throw new TypeError("CommandResponse.env must be a number");
    this.env = data.env;
    this.tactics = ((data.tactics as Tactic[] | undefined) ?? []).map((t) => ({ ...t, usedConstants: t.usedConstants ?? [] }));
    this.declarations = ((data.declarations as DeclarationInfo[] | undefined) ?? []).map(normalizeDeclaration);
    const it = data.infotree as unknown[] | null | undefined;
    this.infotree = it ? it.map((t) => (t instanceof InfoTree ? t : new InfoTree(t as never))) : null;
  }

  /** Copy with some fields replaced (pydantic `model_copy(update=...)`). */
  with(update: Partial<Record<string, unknown>>): CommandResponse {
    return new CommandResponse({ ...this, ...update } as { env: number });
  }
}

/** Response to a {@link ProofStep} or proof-state (un)pickling. */
export class ProofStepResponse extends BaseREPLResponse {
  /** `Completed`, `Incomplete`, `Error`, possibly with details (`Incomplete: contains sorry`). */
  proofStatus!: string;
  /** Proof state after the step. */
  proofState!: number;
  /** Remaining goals. */
  goals: string[] = [];
  /** Traces produced by the step. */
  traces: string[] = [];
  [key: string]: unknown;

  constructor(data: { proofStatus: string; proofState: number; [key: string]: unknown }) {
    super();
    this.init(data);
    if (typeof data.proofState !== "number") throw new TypeError("ProofStepResponse.proofState must be a number");
    if (typeof data.proofStatus !== "string") throw new TypeError("ProofStepResponse.proofStatus must be a string");
    this.proofStatus = data.proofStatus;
    this.proofState = data.proofState;
    this.goals = (data.goals as string[] | undefined) ?? [];
    this.traces = (data.traces as string[] | undefined) ?? [];
  }

  with(update: Partial<Record<string, unknown>>): ProofStepResponse {
    return new ProofStepResponse({ ...this, ...update } as { proofStatus: string; proofState: number });
  }
}

/** An error reported by the REPL itself (e.g. unknown environment). Returned, not thrown. */
export class LeanError {
  message: string;
  [key: string]: unknown;

  constructor(data: { message?: string; [key: string]: unknown } = {}) {
    Object.assign(this, data);
    this.message = data.message ?? "";
  }
}

export type REPLResponse = CommandResponse | ProofStepResponse;
