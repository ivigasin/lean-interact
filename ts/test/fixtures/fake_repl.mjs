#!/usr/bin/env node
// Fake Lean REPL speaking the JSON-over-stdio protocol, for tests.
//
// Commands (`cmd`):
//   "def <name> ..."   adds <name> to the environment
//   "check <name>"     error message unless <name> is defined in the environment
//   "... sorry ..."    reports a sorry with a new proof state
//   "... error ..."    reports an error message
//   "sleep <ms>"       answers after <ms> milliseconds
//   "crash"            exits immediately without answering
// Every non-error response carries `echo` (the received request) and `pid`.

import { readFileSync, writeFileSync, existsSync } from "node:fs";

const envs = []; // env id -> { defs: string[] }
const proofStates = []; // proof state id -> { goals: string[] }

function newEnv(defs) {
  envs.push({ defs });
  return envs.length - 1;
}

function newProofState(goals) {
  proofStates.push({ goals });
  return proofStates.length - 1;
}

function runCmd(req, code) {
  let defs = [];
  if (req.env !== undefined) {
    if (!envs[req.env]) return { message: `Unknown environment.` };
    defs = envs[req.env].defs;
  }
  const messages = [];
  const sorries = [];
  const def = /^def\s+(\S+)/.exec(code);
  if (def) defs = [...defs, def[1]];
  const check = /^check\s+(\S+)/.exec(code);
  if (check && !defs.includes(check[1])) {
    messages.push({ pos: { line: 1, column: 6 }, endPos: { line: 1, column: 6 + check[1].length }, severity: "error", data: `unknown identifier '${check[1]}'` });
  }
  if (code.includes("error")) {
    messages.push({ pos: { line: 1, column: 0 }, endPos: { line: 1, column: 5 }, severity: "error", data: "fake error" });
  }
  if (code.includes("sorry")) {
    messages.push({ pos: { line: 1, column: 8 }, endPos: { line: 1, column: 20 }, severity: "warning", data: "declaration uses `sorry`" });
    sorries.push({ pos: { line: 1, column: 32 }, endPos: { line: 1, column: 37 }, goal: "⊢ 0 = 0", proofState: newProofState(["⊢ 0 = 0"]) });
  }
  const resp = { env: newEnv(defs) };
  if (messages.length) resp.messages = messages;
  if (sorries.length) resp.sorries = sorries;
  return resp;
}

function handle(req) {
  if (req.cmd !== undefined) return runCmd(req, req.cmd);
  if (req.path !== undefined) {
    if (!existsSync(req.path)) return { message: `no such file or directory (error code: 2)\n  file: ${req.path}` };
    return runCmd(req, readFileSync(req.path, "utf8"));
  }
  if (req.tactic !== undefined) {
    if (!proofStates[req.proofState]) return { message: "Unknown proof state." };
    if (req.tactic === "rfl") return { proofState: newProofState([]), goals: [], proofStatus: "Completed" };
    return { proofState: newProofState(["⊢ x = x"]), goals: ["⊢ x = x"], proofStatus: "Incomplete: open goals remain" };
  }
  if (req.pickleTo !== undefined && req.env !== undefined) {
    if (!envs[req.env]) return { message: "Unknown environment." };
    writeFileSync(req.pickleTo, JSON.stringify({ kind: "env", ...envs[req.env] }));
    return { env: req.env };
  }
  if (req.pickleTo !== undefined && req.proofState !== undefined) {
    if (!proofStates[req.proofState]) return { message: "Unknown proof state." };
    writeFileSync(req.pickleTo, JSON.stringify({ kind: "proof", ...proofStates[req.proofState] }));
    return { proofState: req.proofState, goals: proofStates[req.proofState].goals, proofStatus: "Incomplete" };
  }
  if (req.unpickleEnvFrom !== undefined) {
    if (!existsSync(req.unpickleEnvFrom)) return { message: "unpickle: file not found" };
    return { env: newEnv(JSON.parse(readFileSync(req.unpickleEnvFrom, "utf8")).defs) };
  }
  if (req.unpickleProofStateFrom !== undefined) {
    if (!existsSync(req.unpickleProofStateFrom)) return { message: "unpickle: file not found" };
    const goals = JSON.parse(readFileSync(req.unpickleProofStateFrom, "utf8")).goals;
    return { proofState: newProofState(goals), goals, proofStatus: "Incomplete" };
  }
  return { message: "Could not parse as a valid JSON command" };
}

let buffer = "";
let chain = Promise.resolve();

function respond(obj) {
  process.stdout.write(JSON.stringify(obj, null, 2) + "\n\n");
}

process.stdin.setEncoding("utf8");
process.stdin.on("data", (chunk) => {
  buffer += chunk;
  let idx;
  while ((idx = buffer.indexOf("\n\n")) !== -1) {
    const text = buffer.slice(0, idx).trim();
    buffer = buffer.slice(idx + 2);
    if (!text) continue;
    chain = chain.then(async () => {
      let req;
      try {
        req = JSON.parse(text);
      } catch {
        respond({ message: "Could not parse JSON" });
        return;
      }
      if (req.cmd === "crash") process.exit(3);
      const sleep = /^sleep\s+(\d+)/.exec(req.cmd ?? "");
      if (sleep) await new Promise((r) => setTimeout(r, Number(sleep[1])));
      const resp = handle(req);
      // Like the real REPL, errors are a bare {"message": ...} object.
      respond("message" in resp ? resp : { ...resp, echo: req, pid: process.pid });
    });
  }
});
process.stdin.on("end", () => chain.then(() => process.exit(0)));
