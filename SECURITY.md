# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| Latest `1.x` minor | All fixes (bug + security) |
| Previous `1.x` minor | Security fixes, CVSS ≥ 7.0, best-effort |
| Older `1.x` minors | Not supported — please upgrade |

See [`RELEASES.md`](RELEASES.md) for the full support policy.

## Reporting a Vulnerability

**Do not open a public issue or pull request for security vulnerabilities.**

Report privately via GitHub's private vulnerability reporting:

1. Go to <https://github.com/velocitykode/velocity/security/advisories/new>.
2. Fill in the vulnerability details (description, affected versions, reproduction, suggested fix if any).
3. Submit the draft advisory.

Only repository maintainers can see the draft. You will be added as a collaborator on the advisory so you can follow the fix.

### What to include

- **Description** — what's the flaw, and what impact does it have?
- **Affected versions** — which versions of Velocity are vulnerable.
- **Reproduction** — minimal code or HTTP request that triggers the issue.
- **Severity assessment** — CVSS score if you've computed one.
- **Suggested fix** — optional, but welcome.

### Response SLA

- **Acknowledgement** — within 72 hours of receiving your report.
- **Triage and severity assessment** — within 7 days.
- **Fix development** — depends on severity. High-severity issues are prioritized above other work.

## Disclosure Process

Velocity follows a **coordinated disclosure** model:

1. **Report received** — maintainer acknowledges within 72 hours.
2. **Triage** — severity and scope are assessed; CVSS score assigned.
3. **Fix developed** — in the private advisory workspace (temporary private fork created from the advisory). The reporter is kept in the loop.
4. **Embargo period** — typically 7–14 days between fix-ready and public disclosure. Longer for widely deployed critical issues.
5. **Release day**:
   - Fix is merged to `main` and a patch release is tagged on the current minor.
   - For CVSS ≥ 7.0, a backport is tagged on the previous minor line.
   - The advisory is published; GitHub requests a CVE number automatically.
   - Within hours, the Go vulnerability database picks up the advisory — `govulncheck` will now flag affected versions for all consumers.
6. **Post-disclosure** — the reporter is credited in the advisory (unless they request anonymity).

## Scope

**In scope:**

- The `github.com/velocitykode/velocity` module and all its subpackages.
- Documented environment-variable defaults (e.g. insecure defaults that ship out of the box).
- Bundled security middleware: CSRF, auth, crypto, session, cookie handling.
- Framework-issued cookies, headers, and redirects.

**Out of scope:**

- Third-party dependencies — report those upstream.
- Sibling projects (velocity-cli, velocity-installer, veldeploy.com, velwatch.com) — they have their own `SECURITY.md`.
- Demo applications and example code in `docs/`.
- Issues requiring the attacker to already have application-admin access.
- Denial of service via pathological input alone, unless it crashes or corrupts state.
- Social-engineering or supply-chain issues outside the framework's control.

## Credit

Reporters of valid vulnerabilities are credited in the published GitHub Security Advisory under the "Credits" section. Let us know in your report how you'd like to be credited (name, handle, affiliation) or if you prefer to remain anonymous.

## Contact

For questions about this policy that are not themselves vulnerability reports, open a regular GitHub discussion or issue.
