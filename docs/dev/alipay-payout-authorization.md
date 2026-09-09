# Alipay payout authorization BFF

## Public contract

All initiator routes use existing Console session auth. POST routes retain Origin/CSRF checks and require Idempotency-Key.

- POST `/api/v2/console/bff/payout-method/alipay/authorizations`, body `{"expected_agent_id":"<current Agent ID>"}`. The body asserts UI identity; it cannot select an actor. A mismatch rejects with 409.
- GET `/api/v2/console/bff/payout-method/alipay/authorizations/:authorization_id`.
- POST `/api/v2/console/bff/payout-method/alipay/authorizations/:authorization_id/confirm`, body `{}`.
- Public provider callback: GET `/api/v2/console/alipay/authorization/callback`.
- Credential-free result page: GET `/api/v2/console/alipay/authorization/result`.

Responses contain `authorization_id`, `status`, RFC3339 `expires_at`, optional `authorization_url` while pending, and `masked_display` after verification. Status values are pending/authorized/rejected/failed/expired/confirmed. Missing retained state returns 410. Another Console session returns 403 even for the same Agent. All responses are private/no-store.

`failed` means verification could not complete because of a service, configuration, transport, or response-contract error; it does not imply failed real-name verification. Only a provider callback denial/missing code or Commission HTTP 400 `PAYMENT_INVALID_ARGUMENT` is classified as `rejected`. No upstream diagnostics are exposed. Both states are terminal and require a newly initiated attempt, never replay of the consumed callback. Older clients cannot confirm either state; clients should handle unknown statuses without enabling confirmation.

## Data and security

Existing Redis holds ten-minute attempts. The owner is a hash of authenticated Agent and Console session IDs. Separate random query IDs and callback state prevent QR possession from granting query/confirmation access. Initiation uses atomic idempotency; callback consumes state once, calls Commission's body-bound delegated verification endpoint, and records only an authorized/rejected/failed result. It never invokes Wallet Bind. Provider callback codes are briefly retained server-side only after successful verification, never returned or logged; successful confirmation discards them. Reverse-proxy/access logging must redact callback query parameters, and the callback redirects immediately to a no-referrer result page.

Confirmation uses the attempt's stable server-generated Wallet idempotency key. A malformed Wallet success is not displayed as confirmed. Existing Wallet risk, append-only binding, and cooling invariants apply.

The existing HTTP tracer captures full URLs. Its ignore predicate excludes only the Alipay authorization callback to prevent code/state export; other request tracing remains unchanged. Proxy access-log redaction remains a deployment prerequisite outside this source change.

### Caddy callback log redaction

`cloud/caddy/alipay_callback_log_filter.caddy` defines the shared Caddy log
encoder for the canonical callback path. It removes that path's entire query
string at encoding time, leaving the upstream request, status, timing, headers,
and all other URI paths unchanged. It neither disables access logs nor rewrites
the authorization request. Caddy's `log_append` cannot replace `request.uri`.

Install the main-merged snippet under `/etc/caddy/` and import its absolute path
at the top level of the existing root-managed Caddyfile. Within both existing
access loggers (`access-file` and `access-journal`), replace `format json` with
`import alipay_callback_log_filter`. Apply the same encoder to the `default`
logger: upstream failures produce `http.log.error` records outside the access
loggers. Preserve every logger's writers, include/exclude rules, and all site
routes. Do not replace the production Caddyfile with the repository's generic
template.

Before enabling the BFF callback, back up the configuration, validate with the
installed Caddy binary, and reload Caddy. Verify redaction in the file and
journal using only non-credential canaries with invalid state, and check a
non-callback read-only endpoint still retains its ordinary query. Callback and
result responses already use `Referrer-Policy: no-referrer` and redirect to a
credential-free result page. Real authorization codes or states must not be
used for deployment probes.

The isolated local regression suite exercises the production snippet with a
real Caddy process, two log outputs, and a loopback upstream, including a 502
error. Run `CADDY_BIN=/path/to/caddy python3 scripts/cloud/test_alipay_callback_logs.py`.

## Configuration and readiness

EigenFlux BFF needs `ALIPAY_AUTH_APP_ID`, `ALIPAY_AUTH_CALLBACK_URL` (fixed HTTPS callback path above), and `ALIPAY_AUTH_PRODUCTION` (default true). Missing/invalid configuration disables only this authorization flow, not other BFF routes. No merchant private key is added to EigenFlux.

Commission Payment separately needs `ALIPAY_PAYOUT_AUTH_ENABLED=true`, existing Redis configuration, and its existing Alipay credentials. BFF App ID and environment must match Payment. The merchant must enable website user authorization and register the callback domain. The callback and result paths must reach EigenFlux API through the reverse proxy.

Commission verification supports matching UID or application/environment-scoped OpenID evidence, with certification and normal account status required. `clear` means approved provider account-state evidence plus separate existing Wallet risk enforcement, not a global fraud guarantee. The production payee resolver remains absent in Commission; do not enable this binding feature for general production use or claim actual withdrawal readiness until that separate gap is resolved.

## Verification

`go test -race ./tests/tradebff ./api/tradebff`, `go test ./api`, `go vet ./api/tradebff ./tests/tradebff`, and `go build -o build/payout-api ./api`. All OAuth fixtures are local; no live authorization or money movement is performed.
