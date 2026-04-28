# target-repo

Toy TypeScript project used as the **sandbox** that hero-coding's Worker operates inside. Intentionally contains a few bugs as material for the demo user stories.

```bash
cd examples/target-repo
npm install
npm test     # 3 of 6 tests fail on purpose
```

## Known issues (you'll fix these via user stories)

- `parseRange("1-5")` returns `[1,2,3,4]` — off-by-one in the loop bound.
- `formatNumber(-1234)` returns `"-,-1,234"` — sign is concatenated twice with a stray `-`.
- `formatDate(date)` ignores any timezone — always UTC. (Not a bug; story us-001 adds the option.)
