# Security Policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub Security Advisories
for `kgskr/fortigate-externalDNS` (Security tab, "Report a vulnerability"):

<https://github.com/kgskr/fortigate-externalDNS/security/advisories/new>

Do not open a public issue or pull request for a suspected vulnerability, and do
not include real FortiGate URLs, API tokens, private zones, or private IPs in
any report. We will acknowledge the report, work on a fix in a private advisory,
and coordinate disclosure with you.

## Supported versions

Only the latest minor release line receives security fixes. Please upgrade to
the latest release before reporting an issue you found on an older version.

## Handling guidance

This controller holds a FortiGate API token with write access to a DNS
database. Treat that token as sensitive:

- Use a **least-privilege admin profile** for the token, granting only what is
  needed to read and write the DNS database and nothing else. Restrict the
  token's trusted hosts to the cluster egress addresses.
- Prefer a **dedicated DNS database** for the controller. In exclusive mode the
  controller deletes every unmanaged `A`/`AAAA`/`CNAME` row in the database, so
  do not point it at a database that holds hand-managed records.
- **Rotate tokens** regularly and after any suspected exposure; supply the token
  from a Kubernetes Secret rather than flags or committed files.
- Scope watched namespaces so lower-trust resource authors do not indirectly
  gain the FortiGate DNS write credential, and run with `--dry-run` first.
