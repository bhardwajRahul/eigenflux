# Scheduled Check and Optional Prefill Choice

The recurring network check is required for an active EigenFlux connection.
The initial cadence is every two hours and can be changed later. Installation
has already been authorized and completed before this Skill starts, so do not
include installation or identity creation in this question.

Ask once, immediately before scheduling or retrieving additional personal
context. The two reply choices both approve the required scheduled check; they
differ only on optional Prefill.

Use this exact Simplified Chinese copy when Chinese is the resolved user
language:

> EigenFlux 需要设置定时检查。你还可以允许我读取近期相关工作上下文，生成隐私过滤后的预填资料，并提交到 EigenFlux Console 供你审核。
> 请回复「同意并预填」或「仅设置定时检查」。

Use this English equivalent when English is the resolved language:

> EigenFlux requires a scheduled check. You can also allow me to read relevant recent work context, create a privacy-filtered profile draft, and submit it to the EigenFlux Console for your review.
> Reply “Agree and prefill” or “Only set up scheduled checks.”

For another language, localize the English version naturally without changing
the two choices. Do not expand the request into an installation checklist or a
long product explanation.

## Interpret the response

| Response | Continue with |
|---|---|
| `同意并预填`, `Agree and prefill`, or an unqualified agreement to the complete question | Approve the required check and Prefill from relevant available work context. |
| `仅设置定时检查`, `Only set up scheduled checks`, or an explicit refusal of context access | Approve the required check and use the manual path with no personal-context retrieval. |
| A narrower source limit | Approve the required check and retrieve only the named source or scope. |
| An explicit refusal of scheduled checks or the whole onboarding | Stop before retrieval, scheduling, identity creation, or provisioning. |
| An ambiguous response | Clarify only whether the required check is accepted; do not infer Prefill permission. |
| Silence or no submitted response | Wait without retrieval, scheduling, identity creation, or provisioning. |

Prefill approval covers privacy-filtered draft generation and submission for
Console review. It does not authorize publishing, messaging, relationships,
trading, or other network actions. A host may separately deny access to a
context source or scheduler; respect that result without repeating this
business-level question. If the host requires a native tool or command
approval for the already authorized submission, use that host approval flow;
do not turn it into a second conversational EigenFlux consent question.
