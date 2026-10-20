# LeanInteract ports: Go and TypeScript

Ports of [LeanInteract](https://github.com/augustepoiroux/LeanInteract) (Python, v0.11.6), a library
for driving the [Lean 4 REPL](https://github.com/leanprover-community/repl) from code.

| Port | Directory | Docs |
|---|---|---|
| Go | [`go/`](go/) | [go/README.md](go/README.md) |
| TypeScript | [`ts/`](ts/) | [ts/README.md](ts/README.md) |

Both have zero runtime dependencies and the same feature set as the Python library: commands,
tactic mode, declaration and InfoTree extraction, auto-restarting servers with session caches,
server pools, and Lean project management.

[PORTING.md](PORTING.md) records the design, the mapping from Python, and the deliberate
differences. [upstream-issue.md](upstream-issue.md) is a draft report of three session-cache bugs
found while testing against a real REPL; the ports fix them.

## Testing

```
cd go && go test ./...                 # unit + fake-REPL end-to-end
cd ts && npm install && npm test       # unit + fake-REPL end-to-end
```

Against a real Lean REPL (needs `lake` on `PATH`; the first run clones and builds the REPL):

```
cd go && LEAN_INTERACT_E2E=1 go test -run TestE2E ./...
cd ts && npm run test:e2e
```

## License

MIT, same as upstream; see [LICENSE](LICENSE).
