# Security

## Report privately

Contact a [listed maintainer](MAINTAINERS.md) directly to arrange a private report.
Do not publish vulnerabilities in issues or PRs. Include affected versions,
reproduction steps and impact, without credentials or unnecessary customer data.
Coordinate disclosure timing with the maintainers.

## Release support

`v0.1.0` is the initial experimental pre-1.0 release. There is no guaranteed
maintenance window, security-response schedule or production-support commitment.
Security fixes, when available, are published as new versions; existing release
tags are not moved. Backports to development snapshots or older unreleased
schemas are not promised. Include the exact affected tag or commit in reports.

## Source publication

Keep C1+ internal/confidential material, unapproved personal data, credentials and
real production configuration out of source, examples, diagnostics and attachments.
Approved public maintainer contacts and required legal/provenance notices are not
removed by this policy. Use synthetic accounts, reserved example domains and
portable paths; Secret names and environment-variable references must not contain
credential values.

`make verify-hygiene` checks the current deliverable, not Git history, ignored
local state or remote artifacts. Review classification and deployment details
manually. Do not publish a workspace copy: `.git/`, `.tools/`, IDE state and local
build outputs can retain private configuration, authentication and earlier content.
A source cleanup does not sanitize other refs, reflogs, releases or existing copies.
If a credential is found, remove the local/source copy and revoke or rotate it;
deletion alone does not invalidate it. Coordinate any history rewrite separately.

## Trust boundaries

- Restrict catalog, source-template and consumer writes. All namespaces may reference profiles;
  creating consumers can delegate use of native accounts. Bootstrap can execute
  commands on nodes, so these are privileged inputs.
- Profile references can select source templates in any namespace the operator
  can read, independently of consumer watch scope. Administrators manage and
  retain these sources; Composite does not own them or prevent their deletion.
- New bindings check the immutable profile's UID and each source's UID/generation.
  These detect ordinary edits and recreation, not cryptographic content integrity.
  Store cloud credentials using native Secret references: pins do not freeze
  Secret contents or native-provider behavior, and new UIDs require explicit recovery.
- `credentialsRef` injects only a Secret name into explicitly declared native spec
  paths, which consumer overrides cannot alter. Composite reads no Secret contents.
  Protect other identity fields with the profile policy and use namespace-local
  native references. Neither `account` nor `ConfigurationValid` proves a Secret's
  actual cloud identity or permissions.
- Review dynamic-child RBAC. The shipped role permits CRUD within
  `infrastructure.cluster.x-k8s.io`; custom groups need explicit additional rules.
  Watch filters and namespaces do not replace authorization.
- Preserve owner checks, durable pins, preconditioned operations and finalizers.
  An unavailable API does not prove cloud resources have been deleted.
- Keep metrics TLS/authentication/authorization enabled and configure scraper trust.
- Diagnostics intentionally redact native error/payload prose. Native resources,
  underlying error objects and third-party logs still require appropriate access.
- Validate CNI, CSI and cloud-controller behavior for the intended native stack.

`make verify` includes `govulncheck` for both modules; `make verify-hygiene` scans
source for secrets/private endpoints. Keep dependencies and builder images patched.
The [Security workflow](.github/workflows/security.yml) also scans both modules
with gosec and runs Trivy filesystem/IaC/image checks. Gosec and source/config
findings are advisory reports; govulncheck and fixable high/critical image findings
fail their jobs. SARIF remains available as artifacts. Code Scanning upload requires
explicit repository support/opt-in and is disabled for PR events, so forks do not
need write tokens. The release GitHub App's credentials are used only on main,
never in PR checks; no workflow uses pull_request_target to execute untrusted code.

See [automation setup](docs/operations.md#automated-releases) for publishing
permissions and [operations](docs/operations.md) for runtime and recovery details.
