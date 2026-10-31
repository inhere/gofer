Plan rules:
1. Order the steps bottom-up by dependency (storage / model -> protocol / API -> business logic -> callers / frontend); no step may use anything that a later step creates;
2. Prefer vertical slices: after each slice the code compiles and can be verified;
3. Split a step that is expected to change more than 5 files, span more than 2 subsystems, or whose title contains "and / while also";
4. Put a checkpoint (an executable verification command) every 2-3 steps;
5. For key decisions that are cross-module, irreversible, or about concurrency / idempotency / invariants, do one self-review that assumes the author is overconfident, and write down where it is most likely wrong;
6. Collect points that are uncertain or need a person's decision under "Open questions", and keep them out of the implementation.
