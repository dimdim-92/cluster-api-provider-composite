# Contributing

Follow the [Code of Conduct](CODE_OF_CONDUCT.md). Report bugs with a minimal
reproduction, provider versions and relevant redacted observations. Send security
reports privately using [SECURITY.md](SECURITY.md).

Discuss significant behavior changes in an issue or PR. Keep the implementation
focused, explain the trade-offs, and update [design](docs/design.md) or the relevant
usage guide. New providers should use native payloads and CRDs, not Go translators.

## Development

Follow [development](docs/development.md) and the pins in [hack/tools.env](hack/tools.env).

```sh
make envtest-download
make fmt
make gen                 # when generated inputs change
make verify
make verify-hygiene
```

Include regression tests for fixes, especially ownership, retries, recovery and
API serialization. Preserve the standalone API module and include generated
outputs. Describe what was tested; API-server tests do not prove cloud integration.
CRD changes must also update the [API reference](docs/usage.md#api-reference),
affected installation/upgrade instructions, Helm documentation and examples.
Regenerate current target schemas and test attachment lifecycle and source identity guards.

## Pull requests

- Branch from `main`, keep changes focused and explain their purpose.
- Include relevant tests and concise documentation updates.
- Use descriptive Conventional Commit messages, such as `fix: reject invalid bindings`.
  The Commit Lint workflow checks commits and the PR title used for squash merges.
  Message validation is separate from DCO sign-off and GitHub GPG/SSH verification.
- Sign off contribution commits under the [DCO](https://developercertificate.org/):
  `Signed-off-by: Your Name <your.name@example.com>`.
- Obtain maintainer review and passing repository checks before merge.

See [maintainers](MAINTAINERS.md) for coordination and [governance](GOVERNANCE.md)
for responsibilities.
