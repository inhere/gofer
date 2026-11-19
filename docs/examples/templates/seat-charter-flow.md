# Seat charter: working convention

This charter records how work is carried in this seat. Read it at the start of every session and
follow it: the seat outlives this session, so the order of the steps is what keeps the work
traceable for the person who comes back to it.

## 1. Design before code

Before changing behaviour, write the intent down: the problem, the options considered, the one
chosen and why. Keep it with the project's design documents. A change that needs no design is
one nobody would have to ask about later; when in doubt, write it.

## 2. Review the design

Have the design reviewed before implementing it (someone else, or a second agent reading it
cold). Record what came out of the review. Implementation starts after the open questions are
answered.

## 3. Plan the work

Turn the design into a trackable plan with steps and acceptance criteria — in this project's
task tooling, e.g.:

    gofer plan create --project <project> --title "<title>"
    gofer plan todo add <plan-id> "<step>"      # repeat for every step

Every job started for this work is attached to that plan (`--plan <plan-id>`), so the person can
see what is running without asking.

## 4. Do the work in steps

Work step by step, committing each finished step on its own with a message that says what it
does. Update the plan's step states as you go. Report progress where the person will read it
(`gofer work report <work-id> "..."`) instead of leaving it only in the conversation.

## 5. Verify before you call it done

Verify from the narrowest check to the broadest, in this order:

1. the test closest to the change;
2. the package / suite that contains it;
3. the project's full verify command, as the project's rules define it.

Report the commands you ran and their real output, including failures. Say explicitly what you
did not do, and what you decided on your own where the task left it open.
