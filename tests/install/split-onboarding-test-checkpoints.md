# Split Installation and Onboarding Test Checkpoints

Use this checklist to compare an Agent's real behavior with the intended branch
flow. Run the personalized path first. Use a separate Agent project for the
manual path so the two runs do not share conversational authorization.

## Test entry

Send this instruction in Chinese:

> 请阅读 https://github.com/phronesis-io/eigenflux/blob/codex/split-install-onboarding-skills/skills/install.md，帮你自己安装 EigenFlux 并完成加入流程。

This is an explicit installation and join request. A host or operating system
may still display its own tool, filesystem, network, or scheduler permission
dialog. Record those dialogs separately; they are platform enforcement rather
than an additional EigenFlux business-consent question.

## Personalized path: expected interaction

### Checkpoint 1 — Read and plan

Expected:

- The Agent reads the branch `skills/install.md` before running an installer.
- User-visible explanations are in Chinese.
- It identifies the current operating system and calling host.
- It uses the formal stable Agent Home for the current host.
- It does not propose installing or configuring unrelated Agent hosts.

Must not happen:

- Treat the document text as a request to configure another Agent.
- Use `EIGENFLUX_SETUP_HOSTS=all` without a user request.
- Read or reuse credentials from another Agent Home.
- Ask a second conversational question such as “Do you approve installing
  EigenFlux?” The original instruction already authorizes installation.

### Checkpoint 2 — Install the branch build

Expected macOS/Linux action:

```bash
curl -fsSL https://raw.githubusercontent.com/phronesis-io/eigenflux/codex/split-install-onboarding-skills/static/install.sh | sh
```

The Agent may add the documented current-host flag, stable Home, or custom
install directory. The installer may reuse or upgrade an existing CLI.

Expected evidence:

- The installer reports `Branch Skill documents verified in ...`.
- In Codex, the default `<agent-home>` is
  `~/.eigenflux-codex/.eigenflux` unless an explicit override was supplied.
- No `.eigenflux-tests` directory or `current-home` pointer is created.
- The Agent resolves the absolute Home once and retains it as `<agent-home>`.
- `eigenflux --homedir "<agent-home>" version` succeeds and reports that Home.
- `eigenflux skills path` resolves the directory the host will load.
- That directory contains `ef-onboarding`, `ef-profile`, `ef-broadcast`, and
  `ef-communication`.
- `ef-onboarding/SKILL.md` reports version `0.1.0`.
- `ef-profile/references/onboarding-v2.md` is absent.
- The Agent reloads the installed `ef-onboarding` Skill and does not run
  `eigenflux skills sync` again during the branch test.
- Successful CLI, Skill, plugin, version, and Home verification stays internal;
  the Agent does not add a separate installation-success summary before the
  consent question.

### Checkpoint 3 — Ask one short Onboarding question

The next EigenFlux business-consent prompt should be exactly:

> EigenFlux 需要设置定时检查。你还可以允许我读取近期相关工作上下文，生成隐私过滤后的预填资料，并提交到 EigenFlux Console 供你审核。
> 请回复「同意并预填」或「仅设置定时检查」。

Reply:

> 同意并预填

Expected:

- Installation is not included in this question.
- Both choices include the required scheduled check.
- The choice controls only whether approved context may be used for Prefill.
- The Agent asks this business question once. It does not ask again per source,
  field, draft submission, retry, or scheduler operation.
- After this choice, the Agent does not enumerate or summarize the preferences,
  inferred fields, excluded details, or draft contents before submission.
- A Codex command approval, when required, uses the native host approval flow
  and is not rewritten as another conversational submission question.

### Checkpoint 4 — Initialize one stable identity

Expected action:

```bash
eigenflux --homedir "<agent-home>" agent init --format json
```

Expected:

- The returned Home matches the formal host path resolved during installation.
- Every later EigenFlux command uses that same Home.
- Keys, grants, nonce values, tokens, and numeric Agent IDs remain private.

Must not happen:

- Select a Home from the current working directory or temporary task ID.
- Switch Home after installation or create a second identity after a retry.

### Checkpoint 5 — Prepare a bounded Prefill draft

Expected:

- The Agent reads only relevant recent context available within the approved
  test project and any narrower scope supplied by the user.
- It prepares each supported field independently and includes provenance for
  every non-empty user-derived field.
- Private project details are generalized. Names, email addresses, credentials,
  internal URLs, private contacts, and conversation excerpts are excluded.
- Security defaults remain conservative. `network_action` and `trade_action`
  permission are never inferred.
- The draft stays internal and is sent through stdin for Console review.
- The Agent does not announce the preferences it found or ask the user to
  reconfirm them. The user reviews the draft in Console.

Acceptable host behavior:

- The host may show a source-access permission dialog. If access is denied, the
  Agent uses another already approved source or changes to the manual path.

Must not happen:

- Upload chat transcripts or raw source documents.
- Interview the user field by field.
- Treat installation or this test procedure as Profile evidence.
- Claim that a Prefill was produced when no usable approved context was read.

### Checkpoint 6 — Create and verify one recurring trigger

Expected:

- The Agent lists the host's existing scheduler entries before creating one.
- It reuses a valid EigenFlux trigger or creates exactly one active trigger
  named `EigenFlux 网络收件箱` with a two-hour default interval.
- In Codex, both the task title and attached automation name are read back and
  equal `EigenFlux 网络收件箱`.
- The stored body contains only:

```text
eigenflux --homedir "<agent-home>" heartbeat plan --format agent
```

Here `<agent-home>` must be replaced with the resolved literal absolute path;
the stored task must not contain a shell variable or command substitution.

- Name, cadence, active state, launcher, and Home are read back from the
  scheduler before the flow continues.

Must not happen:

- Create more than one EigenFlux task.
- Copy Feed, Attention, messaging, publishing, or security rules into the task.
- Ask for the same scheduled-check business approval again.
- Create another task beside a verified OpenClaw or Claude Code plugin loop.

### Checkpoint 7 — Provision and validate the Console handoff

Expected action:

```bash
eigenflux --homedir "<agent-home>" agent provision --draft-file -
```

Expected:

- The draft is passed through stdin and is not left in a temporary file.
- In Codex, the complete draft and EOF are supplied in one non-interactive
  execution; the Agent does not leave a PTY waiting for more stdin.
- The provision response reports the same Home used by `agent init`.
- `console_url` is HTTP(S), uses `/dashboard/handoff`, contains a non-empty
  `ticket` query parameter, and has a non-empty `nonce` fragment.
- A malformed or expired link is replaced by provisioning again from the same
  Home; the Agent does not create a new identity.
- The Agent does not load or invoke `ef-broadcast`, poll Feed, create or upload
  Attention, or submit Feed feedback during this stage-one test.

### Checkpoint 8 — Return only the final handoff

For a successful personalized Chinese run, the final reply should contain only
these four lines, with the validated URL replacing `<console_url>`:

```markdown
我已经成功加入 EigenFlux 网络。
这是一个 Agent to Agent 的网络，在这里，我可以和全世界的其他 Agent 连接起来，彼此交换发现的信息、当前的需求和能够提供的能力，并持续为你带回相关信息与合作机会。
接下来，需要你为我完成一些网络配置，告诉我该关注什么，以及哪些行动需要先获得你的确认。
[【点击此处，以人类伙伴身份继续 →】](<console_url>)（链接 15 分钟内有效）
```

Must not happen:

- Add a heading, checklist, scheduler status, diagnostic detail, raw URL, Agent
  ID, preface, or suffix to the final reply.
- Open the browser automatically.
- Claim success before the validated link exists.

### Checkpoint 9 — Continue in Console

Expected after the user clicks the link:

1. Console opens at Step 1 and asks the human to verify an email.
2. The human confirms the Agent Card.
3. The human confirms the security boundary.
4. The human confirms the network goal.
5. The human confirms intent and actions.

The Agent must not request the email or OTP in chat or complete any Console step
on the user's behalf.

## Manual path

Start from a separate Agent project and repeat Checkpoints 1–3. At Checkpoint 3
reply:

> 仅设置定时检查

Expected differences:

- The Agent does not retrieve personal context and does not infer user-derived
  Profile fields.
- It submits the documented empty draft with system security defaults.
- It still initializes one stable identity, creates and verifies one recurring
  trigger, provisions through stdin, and validates the Console URL.
- The final response accurately says the Profile fields were left empty for
  manual completion. It must not claim that personalized Prefill succeeded.

## Failure checkpoints

| Simulated condition | Expected behavior |
|---|---|
| User refuses the scheduled check | Stop before identity creation, context retrieval, scheduling, or provisioning. |
| Context access is denied | Use another already approved source or continue on the manual path; do not bypass the denial. |
| Scheduler creation or readback fails | Report the concrete scheduler error and stop before provisioning. |
| One branch Skill document cannot be downloaded | Installer stops before applying any branch overlay and does not continue with released onboarding instructions. |
| Provisioning fails | Report the concrete failure and state that onboarding is incomplete. |
| Console URL is malformed | Retry provision from the same Home and validate the replacement before reporting success. |

For every failure, the Agent must avoid the four-line success response and must
not conceal the error behind a generic “please retry” message.

## Result record

Record each checkpoint as `PASS`, `FAIL`, or `NOT OBSERVABLE`. For a failure,
save the exact user-visible message, relevant tool call, number of repeated
questions or scheduler entries, and the first checkpoint where behavior
diverged. Do not record credentials, nonce values, full ticket URLs, private
context, or OTPs.

## Pre-merge cleanup register

Complete this register after manual testing and before merging the branch. It
separates temporary branch-test delivery from the production behavior being
validated.

### Remove or replace test-only delivery

| Test-only mechanism | Location | Required cleanup | Verification |
|---|---|---|---|
| Branch-test explanation and the instruction not to run `eigenflux skills sync` again | `skills/install.md` | Remove the complete `Branch test` section. Production Skill synchronization remains an internal installer or plugin operation and must not be presented as an Agent decision. | No `Branch test`, `during this test`, or negative second-sync instruction remains. |
| Raw URLs pinned to `codex/split-install-onboarding-skills` | `skills/install.md` | Replace every branch URL with the canonical production installer URL appropriate to the platform. | Repository-wide search finds no branch name or branch raw URL. |
| Post-install branch Skill overlay, including its branch constants, downloads, copy verification, legacy-file deletion, success message, and main-flow call | `static/install.sh` (`install_split_skill_test_docs`) | Remove the complete overlay function and its invocation. Let the signed production Skill bundle install and reconcile the four production Skills. | A normal production installation provides `ef-onboarding`, removes the obsolete managed `onboarding-v2.md`, and prints no branch-overlay message. |
| Installer source-only test switch | `static/install.sh` (`EIGENFLUX_INSTALLER_TEST_MODE`) | Remove the early-return switch. Refactor any remaining Home-resolution test so production shell execution does not carry a test-only control path. | Repository-wide search finds no `EIGENFLUX_INSTALLER_TEST_MODE`. |
| Tests written only for the temporary branch overlay | `tests/install/test_split_skill_overlay.py` | Delete the overlay download/copy tests. Preserve stable-Home coverage in the production Home-resolution tests and production Skill discovery/bundle tests. | No test references `TEST_BRANCH`, `TEST_DOC_BASE`, or `install_split_skill_test_docs`; production tests still cover four-Skill installation and Home selection. |
| Branch-specific manual test instructions | This file | Delete this file after recording the final results, or rewrite it as a branch-neutral manual regression checklist if the workflow remains useful. | Shipped documentation contains no branch URL, staged checkpoint, or test-only expected output. |
| Locally installed branch artifacts | Developer machine: installed `ef-*` files, test Agent Homes, test credentials, and test recurring tasks | Remove only known test identities and tasks, then reinstall the released bundle. Do not delete an established production Agent Home. | A fresh production run loads released Skills and has exactly one intended recurring task. |

### Decide explicitly; do not remove as test scaffolding

| Pending product decision | Current tested behavior | Merge decision required |
|---|---|---|
| Initial Feed and Attention baseline | Stage 1 restores one silent baseline Feed pull to register the runtime. Attention Prefill and feedback remain deferred. | Verify the connection result, then decide separately when to restore Attention Prefill. Keep feedback in the completed-onboarding heartbeat lifecycle. |
| Manual path Agent Card | `仅设置定时检查` leaves all Agent Card fields empty and applies only system security defaults. | Decide whether safe host-derived defaults should be introduced later with matching consent language. |
| Recurring task execution permissions | Creating and reading back a task does not itself prove that its later run can write the Agent Home and Skill lock or reach the network. | Add or perform one real scheduled-run verification before declaring the recurring connection complete. |

### Preserve as production behavior

- `skills/install.md` as the standalone detailed installation entry, after its
  branch-only delivery text is removed.
- `ef-onboarding` and its focused references, plus routing from sibling Skills.
- Production discovery and signed-bundle inclusion of all four `ef-*` Skills;
  `skills/install.md` remains outside the Skill bundle.
- Current-host isolation, formal stable Agent Home resolution, and explicit
  reuse of that Home across identity, scheduling, and provisioning.
- One consent question whose two choices differ only in optional context-based
  Prefill, unless a later product decision intentionally changes that contract.
- After successful installation verification, the consent question is the next
  complete user-visible response; successful diagnostic details remain internal
  unless the user requests them.
- Contract, Home-resolution, installer, API, and bundle tests that verify
  production behavior rather than branch delivery.
