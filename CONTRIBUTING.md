# Contributing

Follow the [Code of Conduct](CODE_OF_CONDUCT.md). Report bugs with a minimal
reproduction, provider versions and relevant redacted observations. Send security
reports privately using [SECURITY.md](SECURITY.md).

Publish only content approved for public release. Do not include C1+ internal
information, customer data, credentials, workstation paths or real production
configuration in source, examples, reports or screenshots. Use synthetic examples and review
context manually; a passing secret scan is not classification approval. Approved
public maintainer contacts, project ownership and required legal notices may remain.
See [source publication](SECURITY.md#source-publication) for local-state and history
boundaries.

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
  `Signed-off-by: Your Name <your.name@example.com>`. The `DCO` check requires a
  footer trailer matching each commit author's name and email, including real
  merge commits and bot-authored commits. There are no bot exemptions.
- Obtain maintainer review and passing repository checks before merge.

Use `git commit --signoff` (or `git commit -s`) for new commits. To add your own
missing sign-off to the latest commit, use `git commit --amend --no-edit --signoff`.
For several commits, an interactive rebase can amend each affected commit. Only
sign off contributions you are entitled to certify; do not invent another author's
certification. After rewriting an already-pushed PR branch, coordinate with other
contributors and push with `--force-with-lease` if its branch rules allow it.

The `DCO` check examines contribution commits in the PR's base-to-head range,
excluding GitHub's synthetic merge commit and pre-existing base history. DCO is a
contribution certification, not a cryptographic commit-signature check.

See [maintainers](MAINTAINERS.md) for coordination and [governance](GOVERNANCE.md)
for responsibilities.
