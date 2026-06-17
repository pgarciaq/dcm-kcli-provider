# Security Policy

## Supported Versions

| Version | Supported          |
|---------|--------------------|
| 0.2.x   | :white_check_mark: |
| < 0.2   | :x:                |

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it
responsibly:

1. **Do not open a public issue.** Security vulnerabilities should be reported
   privately to avoid exposing users to risk before a fix is available.

2. **Email:** Send a report to the maintainer at **pgarciaq@redhat.com** with:
   - A description of the vulnerability
   - Steps to reproduce
   - Impact assessment
   - Suggested fix (if any)

3. **Response timeline:**
   - Acknowledgement within **48 hours**
   - Initial assessment within **5 business days**
   - Fix or mitigation within **30 days** for confirmed vulnerabilities

## Scope

This project is designed for **development, testing, and homelab environments
only**. It is not intended for production workloads. The following are known
limitations, not vulnerabilities:

- **No authentication:** All API endpoints are unauthenticated. Deploy behind a
  firewall or VPN.
- **No TLS:** All traffic is plaintext. Use a TLS-terminating reverse proxy for
  encrypted transport.
- **kweb limitations:** kweb itself has no authentication or authorization.
  Requests forwarded to kweb inherit this limitation.

## Dependency Scanning

This project uses `govulncheck` in CI to scan Go dependencies for known
vulnerabilities. Dependency updates are applied regularly.
