// @gofer-managed-omp-extension — written by `gofer init hooks --agent omp`; edits are overwritten.
//
// omp (oh-my-pi) hooks are TypeScript extensions, so this shim only translates
// omp events into the JSON `gofer hook omp` reads on stdin (the same shape Claude
// Code / Codex send) and relays the answer back:
//   session_start    -> SessionStart       (registers the session with the gofer hub)
//   input            -> UserPromptSubmit   (a human typed: relay auto-off, title)
//   tool_result      -> PostToolUse        (progress + job watches)
//   session_stop     -> Stop               (relay: wait for a web reply, then continue)
//   session_shutdown -> SessionEnd
//
// omp caps every handler at 30s, far below a relay wait, so Stop does not block
// the handler: the wait runs in a detached `gofer hook omp --wait` child and the
// web reply is delivered later with pi.sendUserMessage (followUp starts a new turn
// once the agent is idle). Everything is best effort: a missing gofer binary or
// hub never affects the agent. Jobs submitted through gofer set GOFER_JOB_ID and
// are bypassed by the gofer side.
import { spawn, type ChildProcess } from "node:child_process";

const GOFER = process.env.GOFER_BIN || "gofer";
const STOP_WAIT_SEC = 7140;
const REPLY_PREFIX = "[gofer web 回复] ";

function textOf(message: any): string {
  const content = message?.content;
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((p: any) => p && p.type === "text" && typeof p.text === "string")
    .map((p: any) => p.text)
    .join("");
}

function resultText(content: any): string {
  if (typeof content === "string") return content;
  if (Array.isArray(content)) return textOf({ content });
  return "";
}

// run feeds one payload to `gofer hook omp`; resolves with its stdout (never rejects).
function run(args: string[], payload: Record<string, unknown>, onChild?: (c: ChildProcess) => void): Promise<string> {
  return new Promise((resolve) => {
    let out = "";
    try {
      const child = spawn(GOFER, ["hook", "omp", ...args], { stdio: ["pipe", "pipe", "ignore"], windowsHide: true });
      onChild?.(child);
      child.stdout.on("data", (d) => (out += d.toString()));
      child.on("error", () => resolve(""));
      child.on("close", () => resolve(out));
      child.stdin.on("error", () => {});
      child.stdin.end(JSON.stringify(payload));
    } catch {
      resolve("");
    }
  });
}

export default function (pi: any) {
  let waiter: ChildProcess | null = null;
  let waiterGen = 0;

  const base = (event: string, ctx: any): Record<string, unknown> => ({
    hook_event_name: event,
    session_id: ctx?.sessionManager?.getSessionId?.() ?? "",
    cwd: ctx?.cwd ?? process.cwd(),
    transcript_path: ctx?.sessionManager?.getSessionFile?.() ?? "",
  });

  // cancelWaiter drops the background --wait child. On Windows kill() is a hard
  // TerminateProcess, so the child never gets to report the abort itself (on unix
  // its SIGTERM handler does). With interrupt set and a live waiter, the extension
  // therefore reports the Interrupt FIRST (non-blocking; the returned promise only
  // lets a caller that is about to exit wait for it) and kills afterwards: the
  // server then settles the OPEN turn instead of leaving it to time out. Callers
  // that immediately open a new turn (session_stop) must not interrupt: the late
  // Interrupt could close the turn they are about to create.
  const cancelWaiter = (ctx?: any, interrupt = false): Promise<string> | undefined => {
    waiterGen++;
    let reported: Promise<string> | undefined;
    if (waiter) {
      if (interrupt) reported = run([], base("Interrupt", ctx));
      try { waiter.kill(); } catch {}
      waiter = null;
    }
    return reported;
  };

  pi.on("session_start", async (_e: any, ctx: any) => {
    void run([], base("SessionStart", ctx));
  });

  pi.on("input", async (e: any, ctx: any) => {
    if (e?.source === "extension") return; // our own relayed reply is not a human prompt
    void cancelWaiter(ctx, true); // the human is back at the keyboard; report it, the server closes the open turn
    void run([], { ...base("UserPromptSubmit", ctx), prompt: String(e?.text ?? "") });
  });

  pi.on("tool_result", async (e: any, ctx: any) => {
    void run([], { ...base("PostToolUse", ctx), tool_name: e?.toolName ?? "", tool_output: resultText(e?.content) });
  });

  pi.on("session_stop", async (e: any, ctx: any) => {
    cancelWaiter(); // no Interrupt here: a new turn opens right below
    const gen = waiterGen;
    const payload = { ...base("Stop", ctx), last_assistant_message: textOf(e?.last_assistant_message) };
    // Do not await: the handler budget is 30s. The child decides (relay off ->
    // exits at once) and, when a reply arrives, prints {"decision":"block","reason":...}.
    void run(["--wait", String(STOP_WAIT_SEC)], payload, (c) => { waiter = c; }).then((out) => {
      if (gen !== waiterGen) return; // superseded by a newer prompt/stop
      waiter = null;
      let reason = "";
      try { reason = JSON.parse(out.trim().split("\n").pop() || "{}").reason ?? ""; } catch {}
      if (!reason) return;
      try {
        pi.sendUserMessage(reason.startsWith(REPLY_PREFIX) ? reason : REPLY_PREFIX + reason, { deliverAs: "followUp" });
      } catch {}
    });
  });

  pi.on("session_shutdown", async (_e: any, ctx: any) => {
    // 2s budget: bound the Interrupt + SessionEnd reports so a slow hub cannot delay
    // exit. The Interrupt lands before SessionEnd so the turn is settled first.
    await Promise.race([
      (async () => {
        await cancelWaiter(ctx, true);
        await run([], base("SessionEnd", ctx));
      })(),
      new Promise((r) => setTimeout(r, 1500)),
    ]);
  });
}
