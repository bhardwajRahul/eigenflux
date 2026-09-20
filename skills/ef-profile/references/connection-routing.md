# Existing-account routing

Apply this check before first-time consent, Agent initialization, draft creation,
scheduling, or ordinary provisioning. Preserve the current runtime's stable
Agent Home and selected server throughout.

Resolve the effective Home with `eigenflux version` and the server with
`eigenflux server list`. Check only that server's V2 and legacy credential files
for existence. Keep credential contents private. A local identity key alone
does not prove a registered account. Missing credentials in this Home do not
disprove the user's stated historical account.

When credentials exist, run `eigenflux profile show --format json` with that
Home and server to identify the authenticated Agent. On a V2 authentication
failure, use `eigenflux agent refresh` and retry the read once. Treat unresolved
authentication, network failures, and unreadable local state as diagnostics;
preserve the identity and stop automatic setup.

Route by the user's intent and the available identity:

- Authenticated existing Agent: use `ef-profile`. For Console access, return
  the stable Dashboard link. Recommend switching this Home's account binding
  if the user wants their other existing account. Run `agent switch-account`
  when that intent is explicit; otherwise present the recommendation first.
- Existing Agent with unfinished V2 setup: explain that the existing account
  needs setup completion. Offer the stable Dashboard for continuing that
  account or account switching for another account. Preserve its stored draft;
  never infer a new account solely from incomplete onboarding.
- User reports a historical account but this Home lacks usable V2 credentials:
  recommend historical recovery through `ef-profile`. Generate the recovery
  link once recovery is requested. Keep any required temporary provisioning
  inside that recovery route and use recovery wording.
- No account evidence and no stated historical account: load `ef-onboarding`.
  Resume an interrupted first-time flow only when the conversation explicitly
  establishes that intent; reuse the same Home, identity, and completed steps.

Treat account switching as changing which Agent this Home uses. Treat changing
the current account's email as a separate request; never substitute one for the
other. Keep email verification and final account selection in Console.

For explicit legacy in-place upgrades, retain the existing upgrade workflow
and require `--require-existing-agent`. Preserve the original Agent ID. Route
unusable legacy proof to recovery instead of ordinary new registration.
