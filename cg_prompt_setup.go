package main

// cg_prompt_setup.go — the /setup prompt (#1180, slice #1182).
//
// THE GAP THIS FILLS. Every other prompt this server offers onboards a PROJECT *into* partyline —
// /onboard_project, /new_project, /prd, /planning_agent. None of them gets partyline working in the
// first place. A new user with a capable agent sitting right next to them still had to read a
// 472-line page and hand-execute it.
//
// WHY A PROMPT AND NOT `bootstrap --apply`. The alternative was an --apply flag that writes .env and
// generates secrets on the operator's box. It was rejected deliberately: that puts secret generation
// and file writes in a tool's hands, unattended, on a machine we do not own. `ptln server bootstrap`
// promises "a plan only: nothing here is executed, no file is written, no secret is generated" and
// that promise is a feature. This prompt keeps a human approving every write, which is the same
// posture as everything else here — the daemon proposes and a human disposes, crank leaves branches
// and a human merges, the control plane sends references and never commands.
//
// NOT THREAD-GATED, like the reference prompt. Setup is what you run when nothing is configured yet;
// a prompt that refuses until a thread is bound would be useless precisely when it is needed.

const setupPromptName = "setup"

const setupPromptDesc = "Set partyline up on this machine, one approved step at a time: install, sign in, " +
	"enrol the machine, register a project — or bring up a self-hosted server. Runs the checks, explains " +
	"each step, and never writes anything you have not approved."

// setupSafety is stated once and shared by both paths. These are the rules that make it safe to
// point an agent at someone's machine, so they are not phrased as suggestions.
const setupSafety = "HOW YOU MUST BEHAVE (these are not optional):\n" +
	"- NEVER generate, invent, echo, or repeat back a SECRET VALUE. When a secret is needed, tell the " +
	"human to run `openssl rand -base64 32` themselves and paste the result straight into the file. A " +
	"secret that passes through your context is a secret in a transcript, and transcripts get shared.\n" +
	"- NEVER write, create, or edit a file unless the human approved that specific step in this " +
	"conversation. Approval for one step is not approval for the next.\n" +
	"- ONE STEP AT A TIME. Explain what the step does in plain language, say what it will change, ask, " +
	"and wait. Do not batch steps, and do not run ahead because the next one looks obvious.\n" +
	"- STOP at the first failing check. Name the failure and its fix; do not continue past it and do " +
	"not report success you have not verified.\n" +
	"- Re-run the relevant check after each step and say what it ACTUALLY reports — never what the plan " +
	"intended it to report.\n" +
	"- If a command needs `sudo` or touches anything outside partyline's own directories, say so " +
	"explicitly before asking."

// setupClient — the path almost everyone takes. Someone installing the CLI to use the hosted
// product, which is a far larger audience than self-hosters and needs no server at all.
const setupClientPrompt = "Set partyline up on this machine, working WITH me — you drive the checks, I approve every change.\n\n" +
	setupSafety + "\n\n" +
	"START BY FINDING OUT WHERE I ALREADY AM. Do not assume a fresh machine. Run `ptln doctor` — it is " +
	"read-only, safe, needs no arguments, and reports each of: signed in, this machine enrolled, this " +
	"repo, the context thread, the project, and whether work can run here. Every failing line carries " +
	"the exact command that fixes it. If `ptln` is not on PATH at all, that is the first step, not a " +
	"failure to report.\n\n" +
	"THEN WALK ONLY WHAT IS ACTUALLY MISSING, in this order — each one is a step I approve:\n" +
	"1. INSTALL — `brew install partyline-sh/tap/partyline`, or `curl -fsSL https://partyline.sh/install.sh | sh`. " +
	"Confirm with `ptln version`.\n" +
	"2. SIGN IN — `ptln login`. It is a device-code flow: it prints a code and opens a browser, and I " +
	"approve it there. You cannot complete this for me, and you must not try. Confirm with `ptln whoami`.\n" +
	"3. ENROL THIS MACHINE — the daemon is what actually runs work. Tell me what it will do before " +
	"starting it. Note that a machine belongs to whichever ACCOUNT approved its login, so if I expect it " +
	"on a team and it is missing, that is the thing to check first.\n" +
	"4. REGISTER A PROJECT — point partyline at a repo directory on this machine, so work can be " +
	"dispatched to it.\n" +
	"5. RE-RUN `ptln doctor` and tell me what it reports now. If anything is still failing, say so " +
	"plainly rather than declaring setup complete.\n\n" +
	"IF I ASK ABOUT SELF-HOSTING a server rather than using the hosted one, switch to that path instead: " +
	"`ptln server bootstrap --json` gives you the machine-readable plan for a box.\n\n" +
	"Tell me in one line what you are about to check, then start."

// setupSelfHost — the server path. Everything it needs already exists in the CLI; the job here is
// to consume `bootstrap --json` and walk it, not to re-derive the plan.
const setupSelfHostPrompt = "Bring up a SELF-HOSTED partyline server on this box, working WITH me — you read the plan, " +
	"I approve every change.\n\n" +
	setupSafety + "\n\n" +
	"THE PLAN IS NOT YOURS TO INVENT. Run `ptln server bootstrap --json`. It checks docker, docker " +
	"compose, the stack's ports, free disk, the stack files, and which required environment variables " +
	"this box is missing — then emits the exact ordered commands. It PRINTS ONLY: it never writes .env, " +
	"never generates a secret, never runs docker, never touches the database. Its exit code is 1 when a " +
	"prerequisite is missing, so you can branch on it without parsing text. Its output names VARIABLES, " +
	"never values, which is why it is safe to read aloud.\n\n" +
	"WALK IT LIKE THIS:\n" +
	"1. Show me the failing checks first, in plain language — what is missing and why it matters. Do not " +
	"start the plan while a prerequisite is unmet unless I say to.\n" +
	"2. Go through the ordered steps ONE AT A TIME, explaining each before asking. Copying the stack " +
	"files is low-risk; editing `.env` is where the secrets live and needs the most care.\n" +
	"3. For every secret the plan names: tell me the variable, tell me to run `openssl rand -base64 32`, " +
	"and let ME paste it in. Do not read a generated value back to me, and do not put one in a command " +
	"you print.\n" +
	"4. After the stack is up, run `ptln server doctor` and report which features this box ACTUALLY " +
	"configures — that, not the plan, is the truth about what works.\n" +
	"5. Name what is still unconfigured and what each missing piece costs me, so I can decide whether I " +
	"care. A self-host with no GitHub App is a working install with a smaller feature set, not a failure.\n\n" +
	"The full written guide is at partyline.sh/docs/self-host if I want to read ahead.\n\n" +
	"Start by running the checks and telling me where this box stands."

const setupSelfHostPromptName = "setup_self_host"

const setupSelfHostPromptDesc = "Bring up a self-hosted partyline server on this box: run the prerequisite checks, " +
	"walk the ordered install plan one approved step at a time, then report what the box actually configures."
