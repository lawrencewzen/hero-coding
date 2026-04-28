#!/usr/bin/env node
import { runOnce, watch } from "./dispatcher.js";

const [, , cmd, arg] = process.argv;

async function main(): Promise<void> {
  switch (cmd) {
    case "watch":
      await watch();
      break;
    case "story":
      if (!arg) {
        console.error("usage: hero story <path/to/story.md>");
        process.exit(2);
      }
      await runOnce(arg);
      break;
    default:
      console.log(
        [
          "hero-coding — minimal autonomous coding agent",
          "",
          "Usage:",
          "  hero watch                 watch inbox/ and process stories",
          "  hero story <path>          run a single user story",
        ].join("\n"),
      );
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
